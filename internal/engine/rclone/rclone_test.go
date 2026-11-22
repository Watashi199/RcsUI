package rclone

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"rcsui/internal/engine"
	"rcsui/internal/paths"
)

var (
	tOnce   sync.Once
	tClient *Client
	tErr    error
)

// client initialise librclone une fois pour tout le package de test.
func client(t *testing.T) *Client {
	t.Helper()
	tOnce.Do(func() {
		dir, _ := os.MkdirTemp("", "rcsui-rclone-test")
		// Configuration chiffrée : toute la suite tourne comme en production (M10).
		tClient, tErr = Init(filepath.Join(dir, "rclone.conf"), filepath.Join(dir, "cache"),
			ConfigCrypt{Password: "test-password", Encrypt: true, BackupDir: filepath.Join(dir, "backups")})
		if tErr == nil {
			_, tErr = tClient.CreateRemote("mem", "memory", map[string]any{}, RemoteValidator{})
		}
	})
	if tErr != nil {
		t.Fatal(tErr)
	}
	return tClient
}

type rec struct {
	mu    sync.Mutex
	logs  []engine.LogLine
	items []engine.Item
	prog  []engine.Progress
}

func (r *rec) OnProgress(p engine.Progress) { r.mu.Lock(); r.prog = append(r.prog, p); r.mu.Unlock() }
func (r *rec) OnLog(l engine.LogLine)       { r.mu.Lock(); r.logs = append(r.logs, l); r.mu.Unlock() }
func (r *rec) OnItem(i engine.Item)         { r.mu.Lock(); r.items = append(r.items, i); r.mu.Unlock() }

func setup(t *testing.T) (*Engine, string) {
	c := client(t)
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	r, err := paths.New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	return &Engine{Client: c, Paths: r, PollInterval: 50 * time.Millisecond, StopTimeout: 5 * time.Second}, root
}

func spec(t *testing.T, root, dstPath string, dry bool, opts string) engine.Spec {
	o, err := DecodeOptions([]byte(opts))
	if err != nil {
		t.Fatal(err)
	}
	return engine.Spec{RunID: strings.ReplaceAll(t.Name()+dstPath, "/", "_"), Kind: engine.KindRclone, Mode: engine.ModeCopy,
		DryRun: dry, Options: o,
		Source:      engine.Endpoint{Type: engine.EndpointLocal, Path: filepath.Join(root, "src")},
		Destination: engine.Endpoint{Type: engine.EndpointRemote, Remote: "mem", Path: dstPath}}
}

func run(t *testing.T, e *Engine, s engine.Spec) (engine.Result, *rec) {
	t.Helper()
	if err := e.Validate(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	r := &rec{}
	x, err := e.Start(context.Background(), s, r)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-x.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("timeout")
	}
	return x.Result(), r
}

func TestCopyAndDryRun(t *testing.T) {
	e, root := setup(t)
	os.MkdirAll(filepath.Join(root, "src", "d"), 0o755)
	for i := 0; i < 4; i++ {
		os.WriteFile(filepath.Join(root, "src", "d", "f"+string(rune('a'+i))), make([]byte, 1000*(i+1)), 0o644)
	}
	res, r := run(t, e, spec(t, root, "bucket/dry", true, `{"v":1}`))
	if res.Outcome != engine.OutcomeSucceeded {
		t.Fatalf("dry: %+v", res)
	}
	transfers := 0
	for _, it := range r.items {
		if it.Op == engine.ItemTransfer {
			transfers++
		}
	}
	if transfers != 4 {
		t.Errorf("dry-run items=%+v", r.items)
	}
	entries, _, _ := e.Client.ListRemoteDir(context.Background(), "mem", "bucket", 0)
	for _, en := range entries {
		if en.Name == "dry" {
			t.Error("dry-run wrote to destination")
		}
	}

	res, r = run(t, e, spec(t, root, "bucket/real", false, `{"v":1,"excludes":["fd"]}`))
	if res.Outcome != engine.OutcomeSucceeded || res.Final.FilesDone != 3 || res.Final.BytesDone != 6000 {
		t.Errorf("copy: %+v", res)
	}
	found := false
	for _, l := range r.logs {
		if l.Stream == "rclone" && strings.Contains(l.Msg, "Copied (new)") {
			found = true
		}
	}
	if !found {
		t.Errorf("per-run log capture failed: %+v", r.logs)
	}
	entries, _, err := e.Client.ListRemoteDir(context.Background(), "mem", "bucket/real/d", 0)
	if err != nil || len(entries) != 3 {
		t.Errorf("remote entries=%+v %v", entries, err)
	}
}

func TestCancel(t *testing.T) {
	e, root := setup(t)
	os.MkdirAll(filepath.Join(root, "src"), 0o755)
	os.WriteFile(filepath.Join(root, "src", "big"), make([]byte, 30<<20), 0o644)
	e.Client.call("core/bwlimit", map[string]any{"rate": "2M"})
	defer e.Client.call("core/bwlimit", map[string]any{"rate": "off"})
	s := spec(t, root, "bucket/cancel", false, `{"v":1}`)
	r := &rec{}
	x, err := e.Start(context.Background(), s, r)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if x.Progress().BytesDone == 0 {
		t.Error("no progress before cancel")
	}
	x.Cancel("test")
	select {
	case <-x.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("cancel timeout")
	}
	if x.Result().Outcome != engine.OutcomeCancelled {
		t.Errorf("result=%+v", x.Result())
	}
	if x.ExternalID() == 0 {
		t.Error("no rclone job id")
	}
}

func TestFsStringSafety(t *testing.T) {
	e, root := setup(t)
	c := e.Client
	bad := []engine.Endpoint{
		{Type: engine.EndpointRemote, Remote: ":memory", Path: ""},
		{Type: engine.EndpointRemote, Remote: "mem,opt=x", Path: ""},
		{Type: engine.EndpointRemote, Remote: "nope", Path: ""},
		{Type: engine.EndpointRemote, Remote: "mem", Path: "../../etc"},
		{Type: engine.EndpointRemote, Remote: "mem", Path: "a/../../b"},
		{Type: engine.EndpointLocal, Path: "/etc"},
		{Type: engine.EndpointSSH, Path: "/x"},
	}
	for _, ep := range bad {
		if s, err := BuildFs(ep, e.Paths, c); err == nil {
			t.Errorf("accepted %+v -> %q", ep, s)
		}
	}
	s, err := BuildFs(engine.Endpoint{Type: engine.EndpointRemote, Remote: "mem", Path: "/a//b/./c/"}, e.Paths, c)
	if err != nil || s != "mem:a/b/c" {
		t.Errorf("clean: %q %v", s, err)
	}
	s, err = BuildFs(engine.Endpoint{Type: engine.EndpointLocal, Path: filepath.Join(root, "x")}, e.Paths, c)
	if err != nil || s != filepath.Join(root, "x") {
		t.Errorf("local: %q %v", s, err)
	}
}

func TestRemoteManagementAndMasking(t *testing.T) {
	c := client(t)
	if _, err := c.CreateRemote("loc", "local", map[string]any{}, RemoteValidator{}); err == nil {
		t.Error("local-type remote accepted")
	}
	if _, err := c.CreateRemote("w", "webdav", map[string]any{"url": "https://x", "bearer_token_command": "id"}, RemoteValidator{}); err == nil {
		t.Error("*_command option accepted")
	}
	if _, err := c.CreateRemote("s", "sftp", map[string]any{"host": "h", "ssh": "sh -c id"}, RemoteValidator{}); err == nil {
		t.Error("sftp ssh option accepted")
	}
	if _, err := c.CreateRemote("a", "alias", map[string]any{"remote": "/etc"}, RemoteValidator{}); err == nil {
		t.Error("alias to local path accepted without roots")
	}
	if _, err := c.CreateRemote("bad:name", "memory", map[string]any{}, RemoteValidator{}); err == nil {
		t.Error("bad name accepted")
	}
	res, err := c.CreateRemote("s3test", "s3", map[string]any{"provider": "Minio", "access_key_id": "AKIA123",
		"secret_access_key": "SUPERSECRET", "endpoint": "http://minio:9000"}, RemoteValidator{})
	if err != nil || !res.Done {
		t.Fatalf("create s3: %+v %v", res, err)
	}
	d, err := c.GetRemote("s3test")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "SUPERSECRET") || strings.Contains(string(b), "AKIA123") {
		t.Errorf("secret leaked: %s", b)
	}
	if d.Params["endpoint"] != "http://minio:9000" || d.Params["provider"] != "Minio" {
		t.Errorf("public params missing: %s", b)
	}
	if _, err := c.UpdateRemote("s3test", map[string]any{"endpoint": "http://minio2:9000",
		"secret_access_key": map[string]any{"keep": true}}, RemoteValidator{}); err != nil {
		t.Fatal(err)
	}
	raw, _ := c.call("config/get", map[string]any{"name": "s3test"})
	if raw["secret_access_key"] == "" || raw["endpoint"] != "http://minio2:9000" {
		t.Errorf("update: %v", raw)
	}
	al, err := c.CreateRemote("al", "alias", map[string]any{"remote": "s3test:bucket"}, RemoteValidator{})
	if err != nil || !al.Done {
		t.Errorf("alias to remote: %+v %v", al, err)
	}
	if err := c.DeleteRemote("al"); err != nil {
		t.Error(err)
	}
	if err := c.DeleteRemote("s3test"); err != nil {
		t.Error(err)
	}
	ps, err := c.Providers()
	if err != nil || len(ps) < 20 {
		t.Fatalf("providers: %d %v", len(ps), err)
	}
	for _, p := range ps {
		if p.Type == "local" {
			t.Error("local provider exposed")
		}
		for _, o := range p.Options {
			if IsDeniedOption(p.Type, o.Name) {
				t.Errorf("denied option exposed: %s.%s", p.Type, o.Name)
			}
			if o.Secret && o.Default != nil {
				t.Errorf("secret default exposed: %s.%s", p.Type, o.Name)
			}
		}
	}
	if tr := c.TestRemote(context.Background(), "mem"); !tr.OK {
		t.Errorf("test mem: %+v", tr)
	}
}

func TestItemFromLogAndRedact(t *testing.T) {
	cases := map[string]engine.ItemOp{
		"Copied (new)":                                    engine.ItemTransfer,
		"Copied (replaced existing)":                      engine.ItemTransfer,
		"Skipped copy as --dry-run is set (size 1.953Ki)": engine.ItemTransfer,
		"Deleted": engine.ItemDelete,
		"Skipped delete as --dry-run is set (size 10)": engine.ItemDelete,
	}
	for msg, op := range cases {
		it, ok := ItemFromLog("a/b", msg)
		if !ok || it.Op != op {
			t.Errorf("%q -> %+v %v", msg, it, ok)
		}
	}
	it, _ := ItemFromLog("x", "Skipped copy as --dry-run is set (size 1.953Ki)")
	if it.Size != 1999 {
		t.Errorf("size=%d", it.Size)
	}
	if _, ok := ItemFromLog("", "Copied (new)"); ok {
		t.Error("item without object")
	}
	r := Redact("GET https://user:hunter2@host/x?access_token=abc&x=1 Authorization: Bearer xyz")
	for _, s := range []string{"hunter2", "abc", "xyz"} {
		if strings.Contains(r, s) {
			t.Errorf("not redacted %q in %q", s, r)
		}
	}
}

func TestDecodeOptions(t *testing.T) {
	for _, bad := range []string{`{"v":1,"foo":1}`, `{"v":1,"transfers":0}`, `{"v":1,"maxAge":"1 day"}`, `{"v":1,"excludes":["a\n"]}`, `{"v":2}`} {
		if _, err := DecodeOptions([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if _, err := DecodeOptions([]byte(`{"v":1,"maxAge":"7d","maxSize":"100M","transfers":8}`)); err != nil {
		t.Error(err)
	}
}
