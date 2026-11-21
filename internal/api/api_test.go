package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"rcsui/internal/auth"
	"rcsui/internal/engine"
	"rcsui/internal/events"
	"rcsui/internal/paths"
	"rcsui/internal/runner"
	"rcsui/internal/scheduler"
	"rcsui/internal/store"
)

// fakeEngine : moteur minimal qui termine après avoir émis un log et un item.
type fakeEngine struct{ root string }

type fakeOpts struct{}

func (f *fakeEngine) Kind() engine.Kind                  { return engine.KindRsync }
func (f *fakeEngine) DecodeOptions([]byte) (any, error) { return fakeOpts{}, nil }
func (f *fakeEngine) Validate(_ context.Context, s engine.Spec) error {
	ve := &engine.ValidationError{}
	for field, ep := range map[string]engine.Endpoint{"source": s.Source, "destination": s.Destination} {
		if ep.Type == engine.EndpointLocal && !strings.HasPrefix(ep.Path, f.root) {
			ve.Add(field+".path", "outside allowed roots")
		}
	}
	return ve.Err()
}
func (f *fakeEngine) Preview(s engine.Spec) (engine.Preview, error) {
	return engine.Preview{Argv: []string{"rsync", "--", s.Source.Path, s.Destination.Path}}, nil
}
func (f *fakeEngine) Start(_ context.Context, _ engine.Spec, sink engine.Sink) (engine.Execution, error) {
	x := &fakeExec{done: make(chan struct{})}
	go func() {
		time.Sleep(300 * time.Millisecond)
		sink.OnLog(engine.LogLine{Stream: "stdout", Level: engine.LevelInfo, Msg: "copying"})
		sink.OnItem(engine.Item{Op: engine.ItemTransfer, Path: "a.txt", Size: 3})
		sink.OnProgress(engine.Progress{BytesDone: 3, BytesTotal: 3, FilesDone: 1, FilesTotal: 1})
		time.Sleep(300 * time.Millisecond)
		x.res = engine.Result{Outcome: engine.OutcomeSucceeded, Final: engine.Progress{BytesDone: 3, BytesTotal: 3, FilesDone: 1, FilesTotal: 1}}
		close(x.done)
	}()
	return x, nil
}

type fakeExec struct {
	done chan struct{}
	res  engine.Result
}

func (x *fakeExec) Progress() engine.Progress { return engine.Progress{} }
func (x *fakeExec) Cancel(string)             { /* le faux moteur termine seul */ }
func (x *fakeExec) Done() <-chan struct{}     { return x.done }
func (x *fakeExec) Result() engine.Result     { return x.res }
func (x *fakeExec) PID() int                  { return 0 }
func (x *fakeExec) ExternalID() int64         { return 0 }

type harness struct {
	ts     *httptest.Server
	db     *store.DB
	root   string
	server *Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	os.MkdirAll(root, 0o755)
	root, _ = filepath.EvalSymlinks(root)
	db, err := store.Open(context.Background(), filepath.Join(dir, "t.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	h, _ := auth.HashPassword("initial-password")
	db.CreateUser(context.Background(), store.User{Username: "admin", PasswordHash: h, Role: store.RoleAdmin, MustChangePW: true})
	resolver, _ := paths.New([]string{root})
	b := events.NewBroker()
	mgr := runner.New(runner.Deps{DB: db, Engines: map[engine.Kind]engine.Engine{engine.KindRsync: &fakeEngine{root: root}},
		Broker: b, LogDir: filepath.Join(dir, "logs")}, 2)
	sched := scheduler.New(mgr, db, time.UTC, nil)
	s := &Server{DB: db, Sessions: &auth.Sessions{DB: db, Idle: time.Hour, Absolute: time.Hour, CookieSecure: "false"},
		Limiter: auth.NewLimiter(), Runner: mgr, Scheduler: sched, Broker: b, Paths: resolver, LogDir: filepath.Join(dir, "logs")}
	hs := &harness{db: db, root: root, server: s}
	hs.ts = httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		hs.ts.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		mgr.Shutdown(ctx)
		db.Close()
	})
	return hs
}

type client struct {
	t    *testing.T
	h    *harness
	c    *http.Client
	csrf string
}

func (h *harness) client(t *testing.T) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, h: h, c: &http.Client{Jar: jar}}
}

func (c *client) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.h.ts.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, err := c.c.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (c *client) login(user, pw string) int {
	code, out := c.do("POST", "/api/v1/auth/login", map[string]string{"username": user, "password": pw})
	if code == 200 {
		c.csrf, _ = out["csrfToken"].(string)
	}
	return code
}

func errCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func items(out map[string]any) []any {
	l, _ := out["items"].([]any)
	return l
}

func TestAuthFlow(t *testing.T) {
	h := newHarness(t)
	admin := h.client(t)
	if code, _ := admin.do("GET", "/api/v1/jobs", nil); code != 401 {
		t.Fatalf("unauthenticated jobs: %d", code)
	}
	if code := admin.login("admin", "wrong-password"); code != 401 {
		t.Fatalf("bad login: %d", code)
	}
	if code := admin.login("ADMIN", "initial-password"); code != 200 {
		t.Fatalf("login: %d", code)
	}
	if code, out := admin.do("GET", "/api/v1/jobs", nil); code != 403 || errCode(out) != "password_change_required" {
		t.Fatalf("forced change: %d %v", code, out)
	}
	saved := admin.csrf
	admin.csrf = ""
	if code, out := admin.do("POST", "/api/v1/auth/password", map[string]string{"current": "initial-password", "new": "a-much-better-password"}); code != 403 || errCode(out) != "csrf" {
		t.Fatalf("csrf: %d %v", code, out)
	}
	admin.csrf = saved
	if code, _ := admin.do("POST", "/api/v1/auth/password", map[string]string{"current": "initial-password", "new": "short"}); code != 400 {
		t.Fatalf("weak pw: %d", code)
	}
	if code, _ := admin.do("POST", "/api/v1/auth/password", map[string]string{"current": "initial-password", "new": "a-much-better-password"}); code != 204 {
		t.Fatalf("change pw: %d", code)
	}
	if code, _ := admin.do("GET", "/api/v1/jobs", nil); code != 200 {
		t.Fatalf("after change: %d", code)
	}
	if code, _ := admin.do("POST", "/api/v1/auth/logout", nil); code != 204 {
		t.Fatalf("logout: %d", code)
	}
	if code, _ := admin.do("GET", "/api/v1/auth/me", nil); code != 401 {
		t.Fatalf("after logout: %d", code)
	}
}

func setupAdmin(t *testing.T, h *harness) *client {
	ctx := context.Background()
	u, _ := h.db.GetUserByUsername(ctx, "admin")
	f := false
	h.db.UpdateUser(ctx, u.ID, store.UserPatch{MustChangePW: &f})
	c := h.client(t)
	if c.login("admin", "initial-password") != 200 {
		t.Fatal("admin login")
	}
	return c
}

func addUser(t *testing.T, h *harness, admin *client, name string, role store.Role) (*client, int64) {
	ctx := context.Background()
	if code, out := admin.do("POST", "/api/v1/users", map[string]any{"username": name, "password": name + "-password", "role": role}); code != 201 {
		t.Fatalf("create user: %d %v", code, out)
	}
	u, _ := h.db.GetUserByUsername(ctx, name)
	f := false
	h.db.UpdateUser(ctx, u.ID, store.UserPatch{MustChangePW: &f})
	c := h.client(t)
	if c.login(name, name+"-password") != 200 {
		t.Fatalf("%s login", name)
	}
	return c, u.ID
}

func TestJobsRunsAndSSE(t *testing.T) {
	h := newHarness(t)
	admin := setupAdmin(t, h)
	job := map[string]any{"name": "photos", "engine": "rsync", "mode": "sync",
		"source":      map[string]any{"type": "local", "path": "/etc"},
		"destination": map[string]any{"type": "local", "path": filepath.Join(h.root, "dst")},
		"options":     map[string]any{"v": 1}}
	if code, out := admin.do("POST", "/api/v1/jobs", job); code != 400 || errCode(out) != "validation" {
		t.Fatalf("outside roots: %d %v", code, out)
	}
	job["source"] = map[string]any{"type": "local", "path": filepath.Join(h.root, "src")}
	job["schedule"] = map[string]any{"cron": "0 3 * * *", "enabled": true}
	code, out := admin.do("POST", "/api/v1/jobs", job)
	if code != 201 || out["nextRunAt"] == nil {
		t.Fatalf("create job: %d %v", code, out)
	}
	jp := "/api/v1/jobs/" + itoa(int64(out["id"].(float64)))
	if code, _ := admin.do("POST", "/api/v1/jobs", job); code != 409 {
		t.Fatalf("duplicate name: %d", code)
	}
	job["schedule"] = map[string]any{"cron": "every minute", "enabled": true}
	if code, _ := admin.do("POST", "/api/v1/jobs/validate", job); code != 400 {
		t.Fatalf("bad cron accepted: %d", code)
	}
	job["schedule"] = nil
	if code, out := admin.do("POST", "/api/v1/jobs/validate", job); code != 200 || out["preview"] == nil {
		t.Fatalf("validate: %d %v", code, out)
	}
	if code, out := admin.do("GET", "/api/v1/schedule/preview?cron=0+3+*+*+*", nil); code != 200 || len(out["next"].([]any)) != 5 {
		t.Fatalf("schedule preview: %d %v", code, out)
	}

	sse := openSSE(t, admin, "/api/v1/events")
	defer sse.Close()
	code, out = admin.do("POST", jp+"/run", map[string]any{"dryRun": true})
	if code != 202 {
		t.Fatalf("run: %d %v", code, out)
	}
	runID := out["runId"].(string)
	if code, out := admin.do("POST", jp+"/run", nil); code != 409 || errCode(out) != "job_active" {
		t.Fatalf("double run: %d %v", code, out)
	}
	states := readStates(t, sse, runID, "succeeded")
	if strings.Join(states, ",") != "queued,starting,running,succeeded" {
		t.Errorf("sse states=%v", states)
	}
	time.Sleep(50 * time.Millisecond)
	code, out = admin.do("GET", "/api/v1/runs/"+runID, nil)
	if code != 200 || out["state"] != "succeeded" || out["dryRun"] != true {
		t.Fatalf("get run: %d %v", code, out)
	}
	code, out = admin.do("GET", "/api/v1/runs/"+runID+"/logs?limit=100", nil)
	if code != 200 || out["eof"] != true || !strings.Contains(mustJSON(out["lines"]), "copying") {
		t.Fatalf("logs: %d %v", code, out)
	}
	if _, out = admin.do("GET", "/api/v1/runs/"+runID+"/logs?level=warn", nil); strings.Contains(mustJSON(out["lines"]), "copying") {
		t.Fatalf("logs level filter: %v", out)
	}
	code, out = admin.do("GET", "/api/v1/runs/"+runID+"/items?op=transfer", nil)
	if code != 200 || !strings.Contains(mustJSON(out["items"]), "a.txt") {
		t.Fatalf("items: %d %v", code, out)
	}
	if code, _ := admin.do("POST", "/api/v1/runs/"+runID+"/cancel", nil); code != 409 {
		t.Errorf("cancel terminal: %d", code)
	}
	// Le flux d'un run terminé rejoue son historique.
	rs := openSSE(t, admin, "/api/v1/runs/"+runID+"/events")
	defer rs.Close()
	if st := readStates(t, rs, runID, "succeeded"); len(st) != 4 {
		t.Errorf("run stream replay: %v", st)
	}
	if code, out := admin.do("POST", "/api/v1/runs/"+runID+"/retry", nil); code != 202 {
		t.Errorf("retry: %d %v", code, out)
	}
	if code, out := admin.do("GET", "/api/v1/runs?jobId=1&limit=1", nil); code != 200 || len(items(out)) != 1 || out["nextCursor"] == "" {
		t.Errorf("runs pagination: %v", out)
	}
}

func TestAuthorization(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	admin := setupAdmin(t, h)
	job := map[string]any{"name": "j", "engine": "rsync", "mode": "copy",
		"source":      map[string]any{"type": "local", "path": filepath.Join(h.root, "a")},
		"destination": map[string]any{"type": "local", "path": filepath.Join(h.root, "b")}}
	_, out := admin.do("POST", "/api/v1/jobs", job)
	jobID := int64(out["id"].(float64))
	jp := "/api/v1/jobs/" + itoa(jobID)
	_, out = admin.do("POST", jp+"/run", nil)
	runID := out["runId"].(string)

	op, opID := addUser(t, h, admin, "op", store.RoleOperator)
	if code, _ := op.do("GET", jp, nil); code != 404 {
		t.Errorf("op sees job: %d", code)
	}
	if _, out := op.do("GET", "/api/v1/jobs", nil); len(items(out)) != 0 {
		t.Errorf("op job list: %v", out)
	}
	if _, out := op.do("GET", "/api/v1/runs", nil); len(items(out)) != 0 {
		t.Errorf("op run list: %v", out)
	}
	if code, _ := op.do("GET", "/api/v1/runs/"+runID, nil); code != 404 {
		t.Errorf("op sees run: %d", code)
	}
	if code, _ := op.do("GET", "/api/v1/users", nil); code != 403 {
		t.Errorf("op lists users: %d", code)
	}
	if code, out := admin.do("PUT", jp+"/grants", []map[string]any{{"userId": opID, "permission": "run"}}); code != 200 {
		t.Fatalf("grants: %d %v", code, out)
	}
	if code, _ := op.do("GET", jp, nil); code != 200 {
		t.Errorf("op cannot see granted job: %d", code)
	}
	if code, _ := op.do("PUT", jp, job); code != 403 {
		t.Errorf("op edits job with run grant: %d", code)
	}
	if code, _ := op.do("DELETE", jp, nil); code != 403 {
		t.Errorf("op deletes job: %d", code)
	}
	if code, _ := op.do("PUT", jp+"/grants", []map[string]any{}); code != 403 {
		t.Errorf("op changes grants: %d", code)
	}
	opJob := map[string]any{"name": "op-job", "engine": "rsync", "mode": "copy",
		"source":      map[string]any{"type": "local", "path": filepath.Join(h.root, "a")},
		"destination": map[string]any{"type": "remote", "remote": "b2", "path": "x"}}
	if code, out := op.do("POST", "/api/v1/jobs", opJob); code != 400 || !strings.Contains(mustJSON(out), "not allowed to use this remote") {
		t.Errorf("op uses ungranted remote: %d %v", code, out)
	}
	opJob["destination"] = map[string]any{"type": "local", "path": filepath.Join(h.root, "c")}
	code, out := op.do("POST", "/api/v1/jobs", opJob)
	if code != 201 || out["permission"] != "edit" {
		t.Errorf("op creates own job: %d %v", code, out)
	}

	v, vID := addUser(t, h, admin, "viewer", store.RoleViewer)
	h.db.SetJobGrant(ctx, jobID, store.JobGrant{UserID: vID, Permission: store.PermEdit})
	if code, _ := v.do("POST", jp+"/run", nil); code != 403 {
		t.Errorf("viewer runs job: %d", code)
	}
	if code, _ := v.do("POST", "/api/v1/jobs", opJob); code != 403 {
		t.Errorf("viewer creates job: %d", code)
	}
	if code, _ := v.do("GET", "/api/v1/runs/"+runID+"/logs", nil); code != 200 {
		t.Errorf("viewer cannot read logs: %d", code)
	}
	if code, _ := v.do("GET", "/api/v1/browse?loc=local&path="+h.root, nil); code != 403 {
		t.Errorf("viewer browses: %d", code)
	}

	adminUser, _ := h.db.GetUserByUsername(ctx, "admin")
	if code, _ := admin.do("PATCH", "/api/v1/users/"+itoa(adminUser.ID), map[string]any{"role": "viewer"}); code != 409 {
		t.Errorf("demote last admin: %d", code)
	}
	// Désactivation : les sessions de la cible sont révoquées.
	if code, _ := admin.do("PATCH", "/api/v1/users/"+itoa(opID), map[string]any{"disabled": true}); code != 200 {
		t.Errorf("disable op: %d", code)
	}
	if code, _ := op.do("GET", "/api/v1/jobs", nil); code != 401 {
		t.Errorf("disabled user still authenticated: %d", code)
	}
	if code, out := admin.do("PUT", "/api/v1/settings", map[string]any{"maxConcurrent": 3, "rsyncVanishedIsSuccess": false, "logRetentionDays": 30}); code != 200 {
		t.Errorf("settings: %d %v", code, out)
	}
	if _, out := admin.do("GET", "/api/v1/system/info", nil); out["maxConcurrent"].(float64) != 3 {
		t.Errorf("info: %v", out)
	}
	if code, _ := admin.do("PUT", "/api/v1/settings", map[string]any{"maxConcurrent": 3, "evil": 1}); code != 400 {
		t.Errorf("unknown field accepted: %d", code)
	}
	time.Sleep(800 * time.Millisecond) // fin du run
	if code, _ := admin.do("DELETE", jp, nil); code != 204 {
		t.Errorf("delete job: %d", code)
	}
	if code, _ := admin.do("GET", "/api/v1/runs/"+runID, nil); code != 200 {
		t.Errorf("run after job delete: %d", code)
	}
	if code, out := admin.do("GET", "/api/v1/audit", nil); code != 200 || len(items(out)) < 5 {
		t.Errorf("audit: %v", out)
	}
	if code, out := admin.do("GET", "/api/v1/browse?loc=local&path="+h.root, nil); code != 200 {
		t.Errorf("admin browse: %d %v", code, out)
	}
	if code, _ := admin.do("GET", "/api/v1/browse?loc=local&path=/etc", nil); code != 400 {
		t.Errorf("browse outside roots: %d", code)
	}
}

func TestCrossOriginAndRateLimit(t *testing.T) {
	h := newHarness(t)
	req, _ := http.NewRequest("POST", h.ts.URL+"/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("cross-site login: %d", resp.StatusCode)
	}
	c := h.client(t)
	var last int
	for i := 0; i < 7; i++ {
		last = c.login("admin", "bad-password")
	}
	if last != 429 {
		t.Errorf("rate limit: %d", last)
	}
	if code, _ := c.do("GET", "/api/v1/healthz", nil); code != 200 {
		t.Errorf("healthz: %d", code)
	}
	if code, _ := c.do("GET", "/api/v1/nope", nil); code != 404 {
		t.Errorf("unknown endpoint: %d", code)
	}
}

// --- helpers SSE ---------------------------------------------------------------

type sseStream struct {
	resp *http.Response
	sc   *bufio.Scanner
}

func (s *sseStream) Close() { s.resp.Body.Close() }

func openSSE(t *testing.T, c *client, path string) *sseStream {
	t.Helper()
	req, _ := http.NewRequest("GET", c.h.ts.URL+path, nil)
	resp, err := c.c.Do(req)
	if err != nil || resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("sse open: %v %v", err, resp)
	}
	return &sseStream{resp: resp, sc: bufio.NewScanner(resp.Body)}
}

func readStates(t *testing.T, s *sseStream, runID, until string) []string {
	t.Helper()
	var states []string
	deadline := time.AfterFunc(5*time.Second, func() { s.resp.Body.Close() })
	defer deadline.Stop()
	var typ string
	for s.sc.Scan() {
		line := s.sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			typ = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: ") && typ == "run.state":
			var ev map[string]any
			json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev)
			if ev["runId"] == runID {
				states = append(states, ev["to"].(string))
				if ev["to"] == until {
					return states
				}
			}
		}
	}
	t.Fatalf("stream ended, states=%v", states)
	return nil
}
