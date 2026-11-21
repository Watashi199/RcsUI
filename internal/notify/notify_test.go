package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"rcsui/internal/secrets"
	"rcsui/internal/store"
)

type capture struct {
	mu   sync.Mutex
	reqs []captured
	fail int // nombre de réponses 503 à renvoyer d'abord
}

type captured struct {
	path, auth string
	body       map[string]any
}

func (c *capture) handler(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail > 0 {
		c.fail--
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	c.reqs = append(c.reqs, captured{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: m})
}

func (c *capture) wait(t *testing.T, n int) []captured {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		if len(c.reqs) >= n {
			out := append([]captured(nil), c.reqs...)
			c.mu.Unlock()
			return out
		}
		c.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected %d requests", n)
	return nil
}

func newNotifier(t *testing.T) *Notifier {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	box, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	n := &Notifier{DB: db, Box: box, BaseURL: "https://rcsui.example", Backoff: time.Millisecond}
	if err := n.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Stop(context.Background()) })
	return n
}

func TestValidate(t *testing.T) {
	ok := Config{On: ModeFailures, WebhookURL: "https://hooks.example/abc", NtfyURL: "https://ntfy.sh/topic"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Config{
		{On: "sometimes"},
		{On: ModeAll, WebhookURL: "file:///etc/passwd"},
		{On: ModeAll, WebhookURL: "https://"},
		{On: ModeAll, NtfyURL: "https://ntfy.sh/"},
		{On: ModeAll, NtfyToken: "a\nb"},
	} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestWants(t *testing.T) {
	f := Config{On: ModeFailures}
	if f.Wants(store.StateSucceeded, false) || !f.Wants(store.StateFailed, false) || !f.Wants(store.StatePartial, false) {
		t.Error("failures mode")
	}
	if f.Wants(store.StateFailed, true) {
		t.Error("dry-run notified without includeDryRuns")
	}
	a := Config{On: ModeAll, IncludeDryRuns: true}
	if !a.Wants(store.StateSucceeded, true) || a.Wants(store.StateRunning, false) {
		t.Error("all mode")
	}
	if (Config{On: ModeNever}).Wants(store.StateFailed, false) {
		t.Error("never mode")
	}
}

func TestSplitNtfyAndRedact(t *testing.T) {
	base, topic, err := splitNtfy("https://ntfy.example/sub/alerts?x=1")
	if err != nil || base != "https://ntfy.example/sub" || topic != "alerts" {
		t.Fatalf("%q %q %v", base, topic, err)
	}
	if r := Redact("https://hooks.slack.com/services/T0/B0/secret"); strings.Contains(r, "secret") {
		t.Fatalf("redact leaked: %s", r)
	}
}

func TestSaveLoadEncrypted(t *testing.T) {
	n := newNotifier(t)
	ctx := context.Background()
	c := Config{On: ModeAll, WebhookURL: "https://hooks.example/very-secret", NtfyToken: "tk_secret"}
	if err := n.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	raw, _, _ := n.DB.GetSetting(ctx, SettingKey)
	if strings.Contains(raw, "secret") {
		t.Fatal("configuration stored in clear text")
	}
	n2 := &Notifier{DB: n.DB, Box: n.Box}
	if err := n2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	defer n2.Stop(ctx)
	if n2.Config() != c {
		t.Fatalf("reloaded %+v", n2.Config())
	}
}

func TestDeliveryWebhookAndNtfy(t *testing.T) {
	cap := &capture{fail: 1}
	srv := httptest.NewServer(http.HandlerFunc(cap.handler))
	defer srv.Close()
	n := newNotifier(t)
	ctx := context.Background()
	if err := n.Save(ctx, Config{On: ModeFailures, WebhookURL: srv.URL + "/hook", NtfyURL: srv.URL + "/ntfy/backups", NtfyToken: "tk"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	start := now.Add(-90 * time.Second)
	retry := now.Add(5 * time.Minute)

	// Succès : ignoré en mode "failures".
	n.RunFinished(store.Run{ID: "R0", JobName: "ok", State: store.StateSucceeded}, nil)
	n.RunFinished(store.Run{ID: "R1", JobName: "photos → nas", Engine: "rsync", Trigger: store.TriggerSchedule,
		State: store.StateFailed, Attempt: 2, StartedAt: &start, FinishedAt: &now, ErrorMessage: "connection reset"}, &retry)

	got := cap.wait(t, 2)
	if len(got) != 2 {
		t.Fatalf("got %d requests", len(got))
	}
	hook, ntfy := got[0], got[1]
	if hook.path != "/hook" || hook.body["event"] != "run.finished" || hook.body["url"] != "https://rcsui.example/runs/R1" {
		t.Errorf("webhook: %+v", hook)
	}
	if run, _ := hook.body["run"].(map[string]any); run["id"] != "R1" || run["state"] != "failed" {
		t.Errorf("webhook run: %v", hook.body["run"])
	}
	if hook.body["retryAt"] == nil {
		t.Error("webhook: retryAt missing")
	}
	if ntfy.path != "/ntfy" || ntfy.auth != "Bearer tk" || ntfy.body["topic"] != "backups" {
		t.Errorf("ntfy: %+v", ntfy)
	}
	msg, _ := ntfy.body["message"].(string)
	if !strings.Contains(msg, "attempt 2") || !strings.Contains(msg, "connection reset") || !strings.Contains(msg, "Automatic retry") {
		t.Errorf("ntfy message: %q", msg)
	}
	if ntfy.body["title"] != "RcsUI: photos → nas failed" || ntfy.body["priority"] != float64(4) {
		t.Errorf("ntfy title/priority: %v %v", ntfy.body["title"], ntfy.body["priority"])
	}
}

func TestTestNotificationReportsErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	n := newNotifier(t)
	err := n.Test(context.Background(), Config{On: ModeAll, WebhookURL: srv.URL + "/x"})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v", err)
	}
	if err := n.Test(context.Background(), Config{On: ModeAll}); err == nil {
		t.Fatal("test without channel should fail")
	}
}
