package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"rcsui/internal/auth"
	"rcsui/internal/authz"
	"rcsui/internal/engine/rclone"
	"rcsui/internal/paths"
)

const browseMax = 2000

func (s *Server) requireRclone(w http.ResponseWriter) bool {
	if s.Rclone == nil {
		writeError(w, http.StatusServiceUnavailable, "rclone_disabled", "rclone is disabled on this server")
		return false
	}
	return true
}

func (s *Server) remoteName(w http.ResponseWriter, r *http.Request) (string, bool) {
	n := r.PathValue("name")
	if !rclone.ValidRemoteName(n) {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return "", false
	}
	return n, true
}

func (s *Server) canUseRemote(w http.ResponseWriter, r *http.Request, cur *auth.Current, name string) bool {
	ok, err := authz.CanUseRemote(r.Context(), s.DB, cur.User, name)
	if err != nil {
		s.fail(w, r, err)
		return false
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return false
	}
	return true
}

func (s *Server) validator() rclone.RemoteValidator { return rclone.RemoteValidator{Paths: s.Paths} }

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	if !s.requireRclone(w) {
		return
	}
	if !authz.IsAdmin(cur.User) {
		writeError(w, http.StatusForbidden, "forbidden", "administrator role required")
		return
	}
	ps, err := s.Rclone.Providers()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ps})
}

func (s *Server) handleListRemotes(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	if !s.requireRclone(w) {
		return
	}
	rs, err := s.Rclone.ListRemotes()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := []rclone.RemoteSummary{}
	for _, rm := range rs {
		if ok, _ := authz.CanUseRemote(r.Context(), s.DB, cur.User, rm.Name); ok {
			out = append(out, rm)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleGetRemote(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	if !s.requireRclone(w) {
		return
	}
	name, ok := s.remoteName(w, r)
	if !ok || !s.canUseRemote(w, r, cur, name) {
		return
	}
	d, err := s.Rclone.GetRemote(name)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	if !authz.IsAdmin(cur.User) {
		d.Params = nil // un operator ne voit que le nom et le type
	}
	writeJSON(w, http.StatusOK, d)
}

type remoteIn struct {
	Name   string         `json:"name"`
	Type   string         `json:"type"`
	Params map[string]any `json:"params"`
}

func (s *Server) handleCreateRemote(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	if !s.requireRclone(w) {
		return
	}
	var in remoteIn
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if in.Params == nil {
		in.Params = map[string]any{}
	}
	res, err := s.Rclone.CreateRemote(strings.TrimSpace(in.Name), in.Type, in.Params, s.validator())
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	s.audit(r, cur, "remote.create", "remote:"+in.Name, map[string]any{"type": in.Type, "keys": keys(in.Params)})
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) handleUpdateRemote(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	if !s.requireRclone(w) {
		return
	}
	name, ok := s.remoteName(w, r)
	if !ok {
		return
	}
	var in struct {
		Params map[string]any `json:"params"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	res, err := s.Rclone.UpdateRemote(name, in.Params, s.validator())
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	s.audit(r, cur, "remote.update", "remote:"+name, map[string]any{"keys": keys(in.Params)})
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleContinueRemote(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	if !s.requireRclone(w) {
		return
	}
	name, ok := s.remoteName(w, r)
	if !ok {
		return
	}
	var in struct {
		State  string `json:"state"`
		Result any    `json:"result"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	res, err := s.Rclone.ContinueRemote(name, in.State, in.Result)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	s.audit(r, cur, "remote.continue", "remote:"+name, nil)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleDeleteRemote(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	if !s.requireRclone(w) {
		return
	}
	name, ok := s.remoteName(w, r)
	if !ok {
		return
	}
	used, err := s.DB.JobsUsingRemote(r.Context(), name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(used) > 0 {
		writeError(w, http.StatusConflict, "remote_in_use", "remote is used by: "+strings.Join(used, ", "))
		return
	}
	if err := s.Rclone.DeleteRemote(name); err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	_ = s.DB.SetRemoteGrants(r.Context(), name, nil)
	s.audit(r, cur, "remote.delete", "remote:"+name, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestRemote(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	if !s.requireRclone(w) {
		return
	}
	name, ok := s.remoteName(w, r)
	if !ok || !s.canUseRemote(w, r, cur, name) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.Rclone.TestRemote(ctx, name))
}

func (s *Server) handleGetRemoteGrants(w http.ResponseWriter, r *http.Request, _ *auth.Current) {
	name, ok := s.remoteName(w, r)
	if !ok {
		return
	}
	ids, err := s.DB.ListRemoteGrantUsers(r.Context(), name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"userIds": ids})
}

func (s *Server) handlePutRemoteGrants(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	name, ok := s.remoteName(w, r)
	if !ok {
		return
	}
	var in struct {
		UserIDs []int64 `json:"userIds"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.DB.SetRemoteGrants(r.Context(), name, in.UserIDs); err != nil {
		s.fail(w, r, err)
		return
	}
	s.audit(r, cur, "remote.grants", "remote:"+name, map[string]any{"userIds": in.UserIDs})
	writeJSON(w, http.StatusOK, in)
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// --- explorateur ---------------------------------------------------------------

func (s *Server) handleBrowseRoots(w http.ResponseWriter, _ *http.Request, cur *auth.Current) {
	if !authz.CanBrowseLocal(cur.User) {
		writeJSON(w, http.StatusOK, map[string]any{"roots": []string{}})
		return
	}
	roots := s.Paths.Roots()
	if roots == nil {
		roots = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"roots": roots})
}

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	q := r.URL.Query()
	switch q.Get("loc") {
	case "local":
		if !authz.CanBrowseLocal(cur.User) {
			writeError(w, http.StatusForbidden, "forbidden", "not allowed")
			return
		}
		l, err := s.Paths.List(q.Get("path"), browseMax)
		if err != nil {
			if errorsIsPath(err) {
				s.fail(w, r, err)
				return
			}
			writeError(w, http.StatusBadRequest, "browse", "cannot list directory")
			return
		}
		writeJSON(w, http.StatusOK, l)
	case "remote":
		if !s.requireRclone(w) {
			return
		}
		name := q.Get("remote")
		if !rclone.ValidRemoteName(name) || !s.canUseRemote(w, r, cur, name) {
			if !rclone.ValidRemoteName(name) {
				writeError(w, http.StatusNotFound, "not_found", "not found")
			}
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
		defer cancel()
		entries, truncated, err := s.Rclone.ListRemoteDir(ctx, name, q.Get("path"), browseMax)
		if err != nil {
			writeError(w, http.StatusBadGateway, "browse", rclone.Redact(err.Error()))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"path": q.Get("path"), "entries": entries, "truncated": truncated})
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "loc must be local or remote")
	}
}

func errorsIsPath(err error) bool {
	return errors.Is(err, paths.ErrOutsideRoots) || errors.Is(err, paths.ErrInvalid)
}
