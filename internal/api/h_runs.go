package api

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rcsui/internal/auth"
	"rcsui/internal/authz"
	"rcsui/internal/engine"
	"rcsui/internal/events"
	"rcsui/internal/runner"
	"rcsui/internal/store"
)

type runOut struct {
	store.Run
	Live       *runner.Live        `json:"live,omitempty"`
	Permission store.JobPermission `json:"permission"`
}

// runAccess retourne la permission de l'utilisateur sur le run (via son job).
// Un run dont le job a été supprimé n'est visible que des admins.
func (s *Server) runAccess(r *http.Request, cur *auth.Current, run store.Run) (store.JobPermission, error) {
	if authz.IsAdmin(cur.User) {
		return store.PermEdit, nil
	}
	if run.JobID == nil {
		return "", nil
	}
	j, err := s.DB.GetJob(r.Context(), *run.JobID)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return authz.JobPermission(r.Context(), s.DB, cur.User, j)
}

func (s *Server) loadRun(w http.ResponseWriter, r *http.Request, cur *auth.Current, need store.JobPermission) (store.Run, store.JobPermission, bool) {
	id := r.PathValue("id")
	if len(id) != 26 {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return store.Run{}, "", false
	}
	run, err := s.DB.GetRun(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return run, "", false
	}
	p, err := s.runAccess(r, cur, run)
	if err != nil {
		s.fail(w, r, err)
		return run, "", false
	}
	if !authz.Allows(p, store.PermView) {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return run, "", false
	}
	if !authz.Allows(p, need) {
		writeError(w, http.StatusForbidden, "forbidden", "insufficient permission")
		return run, "", false
	}
	return run, p, true
}

func (s *Server) withLive(run store.Run, p store.JobPermission) runOut {
	o := runOut{Run: run, Permission: p}
	if l, ok := s.Runner.Snapshot(run.ID); ok {
		o.Live = &l
		o.State = l.State
	}
	return o
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	q := r.URL.Query()
	f := store.RunFilter{Before: q.Get("cursor")}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	if v := q.Get("jobId"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid jobId")
			return
		}
		f.JobID = &id
	}
	for _, st := range strings.Split(q.Get("state"), ",") {
		if st = strings.TrimSpace(st); st != "" {
			f.States = append(f.States, store.RunState(st))
		}
	}
	perms := map[int64]store.JobPermission{}
	if !authz.IsAdmin(cur.User) {
		jobs, err := s.DB.ListJobs(r.Context())
		if err != nil {
			s.fail(w, r, err)
			return
		}
		vis, err := authz.VisibleJobs(r.Context(), s.DB, cur.User, jobs)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		f.JobIDs = []int64{}
		for id, p := range vis {
			f.JobIDs = append(f.JobIDs, id)
			perms[id] = p
		}
	}
	runs, err := s.DB.ListRuns(r.Context(), f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]runOut, 0, len(runs))
	for _, run := range runs {
		p := store.PermEdit
		if run.JobID != nil && !authz.IsAdmin(cur.User) {
			p = perms[*run.JobID]
		}
		out = append(out, s.withLive(run, p))
	}
	next := ""
	if len(runs) > 0 && len(runs) == effectiveLimit(f.Limit) {
		next = runs[len(runs)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "nextCursor": next})
}

func effectiveLimit(l int) int {
	if l <= 0 || l > 500 {
		return 50
	}
	return l
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	run, p, ok := s.loadRun(w, r, cur, store.PermView)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.withLive(run, p))
}

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	run, _, ok := s.loadRun(w, r, cur, store.PermRun)
	if !ok {
		return
	}
	if err := s.Runner.Cancel(run.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.audit(r, cur, "run.cancel", "run:"+run.ID, map[string]any{"job": run.JobName})
	writeJSON(w, http.StatusAccepted, map[string]string{"runId": run.ID})
}

func (s *Server) handleRetryRun(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	run, p, ok := s.loadRun(w, r, cur, store.PermRun)
	if !ok {
		return
	}
	// Relancer un resync bisync revient à en lancer un : même droit (edit)
	// que POST /jobs/{id}/run {"resync": true}.
	var snap runner.SpecSnapshot
	if err := json.Unmarshal([]byte(run.SpecJSON), &snap); err == nil && snap.Resync && !authz.Allows(p, store.PermEdit) {
		writeError(w, http.StatusForbidden, "forbidden", "retrying a resync requires the edit permission on this job")
		return
	}
	uid := cur.User.ID
	id, err := s.Runner.Retry(r.Context(), run.ID, &uid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.audit(r, cur, "run.retry", "run:"+run.ID, map[string]any{"newRun": id})
	writeJSON(w, http.StatusAccepted, map[string]string{"runId": id})
}

// --- logs & items --------------------------------------------------------------

func (s *Server) runFile(run store.Run, items bool) (string, error) {
	if run.LogPath == "" {
		return "", fs.ErrNotExist
	}
	rel := run.LogPath
	if items {
		rel = strings.TrimSuffix(rel, ".ndjson") + ".items.ndjson"
	}
	p := filepath.Join(s.LogDir, filepath.Clean(rel))
	if !strings.HasPrefix(p, filepath.Clean(s.LogDir)+string(filepath.Separator)) {
		return "", fs.ErrNotExist
	}
	return p, nil
}

var levelRank = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

// readNDJSON lit des lignes depuis un offset ; filter peut ignorer des lignes.
func readNDJSON(path string, from int64, limit int, filter func([]byte) bool) (lines []json.RawMessage, next int64, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, from, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, from, 0, err
	}
	size = st.Size()
	if from > size || from < 0 {
		from = size
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return nil, from, size, err
	}
	br := bufio.NewReaderSize(f, 64*1024)
	next = from
	lines = []json.RawMessage{}
	for len(lines) < limit {
		b, err := br.ReadBytes('\n')
		if len(b) == 0 || b[len(b)-1] != '\n' {
			break // ligne incomplète (écriture en cours) ou EOF
		}
		next += int64(len(b))
		if filter == nil || filter(b) {
			lines = append(lines, json.RawMessage(append([]byte(nil), b[:len(b)-1]...)))
		}
		if err != nil {
			break
		}
	}
	return lines, next, size, nil
}

func parseLimit(v string, def, max int) int {
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func (s *Server) handleRunLogs(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	run, _, ok := s.loadRun(w, r, cur, store.PermView)
	if !ok {
		return
	}
	q := r.URL.Query()
	from, _ := strconv.ParseInt(q.Get("from"), 10, 64)
	limit := parseLimit(q.Get("limit"), 500, 5000)
	minLvl := levelRank[q.Get("level")]
	p, err := s.runFile(run, false)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"lines": []any{}, "next": 0, "eof": run.State.Terminal()})
		return
	}
	var filter func([]byte) bool
	if minLvl > 0 {
		filter = func(b []byte) bool {
			var rec runner.LogRecord
			return json.Unmarshal(b, &rec) == nil && levelRank[string(rec.Lvl)] >= minLvl
		}
	}
	lines, next, size, err := readNDJSON(p, from, limit, filter)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.fail(w, r, err)
		return
	}
	_, active := s.Runner.Snapshot(run.ID)
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines, "next": next, "eof": !active && next >= size})
}

func (s *Server) handleRunLogsDownload(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	run, _, ok := s.loadRun(w, r, cur, store.PermView)
	if !ok {
		return
	}
	p, err := s.runFile(run, false)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "no log")
		return
	}
	f, err := os.Open(p)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "no log")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="run-%s.ndjson"`, run.ID))
	_, _ = io.Copy(w, f)
}

func (s *Server) handleRunItems(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	run, _, ok := s.loadRun(w, r, cur, store.PermView)
	if !ok {
		return
	}
	q := r.URL.Query()
	from, _ := strconv.ParseInt(q.Get("from"), 10, 64)
	limit := parseLimit(q.Get("limit"), 200, 2000)
	op := q.Get("op")
	p, err := s.runFile(run, true)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}, "next": 0, "eof": true})
		return
	}
	var filter func([]byte) bool
	if op != "" {
		filter = func(b []byte) bool {
			var it engine.Item
			return json.Unmarshal(b, &it) == nil && string(it.Op) == op
		}
	}
	lines, next, size, err := readNDJSON(p, from, limit, filter)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.fail(w, r, err)
		return
	}
	_, active := s.Runner.Snapshot(run.ID)
	writeJSON(w, http.StatusOK, map[string]any{"items": lines, "next": next, "eof": !active && next >= size})
}

// --- SSE ---------------------------------------------------------------------

const (
	sseHeartbeat   = 15 * time.Second
	sseRecheck     = 60 * time.Second
	sseWriteWindow = 10 * time.Second
)

type sseWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func (s sseWriter) send(id, typ string, data any) error {
	_ = s.rc.SetWriteDeadline(time.Now().Add(sseWriteWindow))
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if id != "" {
		if _, err := fmt.Fprintf(s.w, "id: %s\n", id); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", typ, b); err != nil {
		return err
	}
	return s.rc.Flush()
}

func (s sseWriter) comment(c string) error {
	_ = s.rc.SetWriteDeadline(time.Now().Add(sseWriteWindow))
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", c); err != nil {
		return err
	}
	return s.rc.Flush()
}

// stream sert un flux SSE. allowed filtre les événements ; recheck revalide
// la session et rafraîchit la visibilité (retourne false pour couper le flux).
func (s *Server) stream(w http.ResponseWriter, r *http.Request, runID string, allowed func(events.Event) bool, recheck func() bool) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	sw := sseWriter{w: w, rc: http.NewResponseController(w)}

	lastID := r.Header.Get("Last-Event-ID")
	if lastID == "" {
		lastID = r.URL.Query().Get("lastEventId")
	}
	sub, replay, resync := s.Broker.Subscribe(runID, lastID)
	defer s.Broker.Unsubscribe(sub)

	if _, err := fmt.Fprint(w, "retry: 3000\n\n"); err != nil {
		return
	}
	if resync {
		if sw.send("", "resync", map[string]string{"reason": "history not available"}) != nil {
			return
		}
	}
	for _, e := range replay {
		if allowed(e) && sw.send(e.ID, e.Type, e.Data) != nil {
			return
		}
	}
	if sw.comment("ready") != nil {
		return
	}
	hb := time.NewTicker(sseHeartbeat)
	defer hb.Stop()
	rc := time.NewTicker(sseRecheck)
	defer rc.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-sub.C:
			if !ok {
				// Abonné trop lent : le client se reconnectera avec Last-Event-ID.
				return
			}
			if allowed(e) && sw.send(e.ID, e.Type, e.Data) != nil {
				return
			}
		case <-hb.C:
			if sw.comment("ping") != nil {
				return
			}
		case <-rc.C:
			if !recheck() {
				_ = sw.send("", "auth", map[string]string{"reason": "session expired or access revoked"})
				return
			}
		}
	}
}

// sessionStillValid revalide l'identifiant utilisé par la requête : jeton
// d'API (expiration, révocation, compte désactivé) ou cookie de session.
func (s *Server) sessionStillValid(r *http.Request) bool {
	if raw, ok := auth.BearerToken(r); ok {
		_, err := s.Sessions.LookupAPIToken(r, raw)
		return err == nil
	}
	_, err := s.Sessions.Lookup(r)
	return err == nil
}

func (s *Server) handleGlobalEvents(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	var visible map[int64]store.JobPermission
	refresh := func() bool {
		if authz.IsAdmin(cur.User) {
			return true
		}
		jobs, err := s.DB.ListJobs(r.Context())
		if err != nil {
			return false
		}
		v, err := authz.VisibleJobs(r.Context(), s.DB, cur.User, jobs)
		if err != nil {
			return false
		}
		visible = v
		return true
	}
	if !refresh() {
		writeError(w, http.StatusInternalServerError, "internal", "cannot compute visibility")
		return
	}
	allowed := func(e events.Event) bool {
		if visible == nil {
			return true
		}
		_, ok := visible[e.JobID]
		return ok
	}
	s.stream(w, r, "", allowed, func() bool { return s.sessionStillValid(r) && refresh() })
}

func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	run, _, ok := s.loadRun(w, r, cur, store.PermView)
	if !ok {
		return
	}
	recheck := func() bool {
		if !s.sessionStillValid(r) {
			return false
		}
		p, err := s.runAccess(r, cur, run)
		return err == nil && authz.Allows(p, store.PermView)
	}
	s.stream(w, r, run.ID, func(events.Event) bool { return true }, recheck)
}
