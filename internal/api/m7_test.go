package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"rcsui/internal/metrics"
	"rcsui/internal/notify"
	"rcsui/internal/runner"
	"rcsui/internal/secrets"
	"rcsui/internal/store"
)

func TestJobRetryAndCatchUpFields(t *testing.T) {
	h := newHarness(t)
	admin := setupAdmin(t, h)
	job := map[string]any{"name": "retry", "engine": "rsync", "mode": "copy",
		"source":      map[string]any{"type": "local", "path": filepath.Join(h.root, "src")},
		"destination": map[string]any{"type": "local", "path": filepath.Join(h.root, "dst")},
		"options":     map[string]any{"v": 1}, "retryMax": 11}
	if code, out := admin.do("POST", "/api/v1/jobs", job); code != 400 || errCode(out) != "validation" {
		t.Fatalf("retryMax 11 accepted: %d %v", code, out)
	}
	job["retryMax"], job["retryDelaySec"] = 3, 5
	if code, _ := admin.do("POST", "/api/v1/jobs", job); code != 400 {
		t.Fatalf("retryDelaySec 5 accepted: %d", code)
	}
	delete(job, "retryDelaySec")
	job["catchUp"] = true
	code, out := admin.do("POST", "/api/v1/jobs", job)
	if code != 201 || out["retryMax"] != float64(3) || out["retryDelaySec"] != float64(300) || out["catchUp"] != true ||
		out["exclusiveDestination"] != false {
		t.Fatalf("create: %d %v", code, out)
	}
	j, err := h.db.GetJob(context.Background(), int64(out["id"].(float64)))
	if err != nil || j.RetryMax != 3 || j.RetryDelaySec != 300 || !j.CatchUp || j.ExclusiveDest {
		t.Fatalf("stored job: %+v %v", j, err)
	}
	// Option « destination exclusive » (M9.4), désactivée par défaut.
	job["exclusiveDestination"] = true
	if code, out := admin.do("PUT", fmt.Sprintf("/api/v1/jobs/%d", j.ID), job); code != 200 || out["exclusiveDestination"] != true {
		t.Fatalf("update: %d %v", code, out)
	}
	if j, _ = h.db.GetJob(context.Background(), j.ID); !j.ExclusiveDest {
		t.Fatal("exclusiveDestination not stored")
	}
	rec := httptest.NewRecorder()
	h.server.fail(rec, httptest.NewRequest("POST", "/", nil), &runner.DestinationBusyError{JobName: "w"})
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"destination_busy"`) {
		t.Errorf("destination busy: %d %s", rec.Code, rec.Body)
	}
}

func TestNotificationSettings(t *testing.T) {
	h := newHarness(t)
	box, _ := secrets.New(make([]byte, 32))
	n := &notify.Notifier{DB: h.db, Box: box}
	if err := n.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer n.Stop(context.Background())
	h.server.Notifier = n

	var mu sync.Mutex
	var hits []map[string]any
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		mu.Lock()
		hits = append(hits, m)
		mu.Unlock()
	}))
	defer hook.Close()

	admin := setupAdmin(t, h)
	secretURL := hook.URL + "/T000/very-secret-path"
	in := map[string]any{"on": "failures", "webhookUrl": secretURL, "ntfyUrl": "https://ntfy.example/alerts", "ntfyToken": "tk_123"}
	code, out := admin.do("PUT", "/api/v1/settings/notifications", in)
	if code != 200 {
		t.Fatalf("put: %d %v", code, out)
	}
	code, out = admin.do("GET", "/api/v1/settings/notifications", nil)
	raw := mustJSON(out)
	if code != 200 || strings.Contains(raw, "very-secret") || strings.Contains(raw, "tk_123") || strings.Contains(raw, "alerts") {
		t.Fatalf("secrets leaked in GET: %s", raw)
	}
	if out["webhookSet"] != true || out["ntfyTokenSet"] != true || out["on"] != "failures" {
		t.Fatalf("get: %v", out)
	}
	// null conserve, "" efface.
	if code, out := admin.do("PUT", "/api/v1/settings/notifications", map[string]any{"on": "all", "ntfyUrl": "", "ntfyToken": ""}); code != 200 ||
		out["webhookSet"] != true || out["ntfySet"] != false || out["ntfyTokenSet"] != false {
		t.Fatalf("partial update: %d %v", code, out)
	}
	if c := n.Config(); c.WebhookURL != secretURL || c.On != notify.ModeAll {
		t.Fatalf("config after update: %+v", c)
	}
	if code, out := admin.do("PUT", "/api/v1/settings/notifications", map[string]any{"on": "all", "webhookUrl": "ftp://x"}); code != 400 {
		t.Fatalf("bad url: %d %v", code, out)
	}
	// Audit : jamais l'URL.
	entries, _ := h.db.ListAudit(context.Background(), 0, 50)
	if strings.Contains(mustJSON(entries), "very-secret") {
		t.Fatal("secret in audit log")
	}

	if code, out := admin.do("POST", "/api/v1/settings/notifications/test", map[string]any{}); code != 200 {
		t.Fatalf("test: %d %v", code, out)
	}
	mu.Lock()
	got := len(hits)
	ev := ""
	if got > 0 {
		ev, _ = hits[0]["event"].(string)
	}
	mu.Unlock()
	if got != 1 || ev != "test" {
		t.Fatalf("test webhook hits=%d event=%q", got, ev)
	}

	// Réservé aux administrateurs.
	op, _ := addUser(t, h, admin, "op", store.RoleOperator)
	if code, _ := op.do("GET", "/api/v1/settings/notifications", nil); code != 403 {
		t.Fatalf("operator: %d", code)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	h := newHarness(t)
	// Sans jeton : non monté (aucune métrique servie).
	resp, err := http.Get(h.ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(b), "rcsui_") {
		t.Fatal("metrics exposed without token")
	}

	m := &metrics.Metrics{DB: h.db, Runner: h.server.Runner, Build: metrics.BuildInfo{Version: "test"}}
	h.server.Metrics = m.Handler("0123456789abcdef")
	ts := httptest.NewServer(h.server.Handler())
	defer ts.Close()
	resp, err = http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer 0123456789abcdef")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), `rcsui_build_info{version="test"`) || !strings.Contains(string(b), "rcsui_max_concurrent_runs 2") {
		t.Fatalf("metrics: %d\n%s", resp.StatusCode, b)
	}
}
