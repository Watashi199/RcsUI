//go:build unix

package rsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"rcsui/internal/engine"
	"rcsui/internal/paths"
)

type recSink struct {
	mu    sync.Mutex
	prog  []engine.Progress
	logs  []engine.LogLine
	items []engine.Item
}

func (r *recSink) OnProgress(p engine.Progress) {
	r.mu.Lock()
	r.prog = append(r.prog, p)
	r.mu.Unlock()
}
func (r *recSink) OnLog(l engine.LogLine) { r.mu.Lock(); r.logs = append(r.logs, l); r.mu.Unlock() }
func (r *recSink) OnItem(i engine.Item)   { r.mu.Lock(); r.items = append(r.items, i); r.mu.Unlock() }

func needRsync(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
}

func newTestEngine(t *testing.T) (*Engine, string) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	r, err := paths.New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	return &Engine{Paths: r, KillGrace: 2 * time.Second, ProgressInterval: 50 * time.Millisecond}, root
}

func write(t *testing.T, p string, n int) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func spec(root string, mode engine.Mode, dry bool, o *Options) engine.Spec {
	return engine.Spec{RunID: "T1", Kind: engine.KindRsync, Mode: mode, DryRun: dry, Options: o,
		Source:      engine.Endpoint{Type: engine.EndpointLocal, Path: filepath.Join(root, "src"), ContentsOnly: true},
		Destination: engine.Endpoint{Type: engine.EndpointLocal, Path: filepath.Join(root, "dst")}}
}

func TestEngineSyncAndDryRun(t *testing.T) {
	needRsync(t)
	e, root := newTestEngine(t)
	for i := 0; i < 5; i++ {
		write(t, filepath.Join(root, "src", "d", "f"+strconv.Itoa(i)), 1000*(i+1))
	}
	write(t, filepath.Join(root, "dst", "stale"), 10)

	sink := &recSink{}
	x, err := e.Start(context.Background(), spec(root, engine.ModeSync, true, &Options{V: 1}), sink)
	if err != nil {
		t.Fatal(err)
	}
	<-x.Done()
	res := x.Result()
	if res.Outcome != engine.OutcomeSucceeded || *res.ExitCode != 0 {
		t.Fatalf("dry-run result %+v logs=%v", res, sink.logs)
	}
	var transfers, deletes int
	for _, it := range sink.items {
		switch it.Op {
		case engine.ItemTransfer:
			transfers++
		case engine.ItemDelete:
			deletes++
		}
	}
	if transfers != 5 || deletes != 1 {
		t.Errorf("dry-run items transfers=%d deletes=%d", transfers, deletes)
	}
	if _, err := os.Stat(filepath.Join(root, "dst", "d")); err == nil {
		t.Error("dry-run wrote files")
	}

	sink = &recSink{}
	x, err = e.Start(context.Background(), spec(root, engine.ModeSync, false, &Options{V: 1}), sink)
	if err != nil {
		t.Fatal(err)
	}
	<-x.Done()
	res = x.Result()
	if res.Outcome != engine.OutcomeSucceeded || res.Final.FilesDone != 5 || res.Final.BytesDone != 15000 || res.Final.Deletes != 1 {
		t.Errorf("sync result %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "dst", "stale")); err == nil {
		t.Error("stale file not deleted")
	}
}

func TestEngineValidation(t *testing.T) {
	e, root := newTestEngine(t)
	s := spec(root, engine.ModeSync, false, &Options{V: 1})
	s.Destination.Path = filepath.Join(root, "src", "inner")
	if err := e.Validate(context.Background(), s); err == nil {
		t.Error("destination inside source accepted")
	}
	s = spec(root, engine.ModeCopy, false, &Options{V: 1})
	s.Source.Path = "/etc"
	if err := e.Validate(context.Background(), s); err == nil {
		t.Error("path outside roots accepted")
	}
	s = spec(root, engine.ModeCopy, false, &Options{V: 1})
	s.Destination = engine.Endpoint{Type: engine.EndpointSSH, Path: "/x", SSH: &engine.SSHTarget{Host: "h", Port: 22, User: "u", Identity: engine.SSHIdentity{Mounted: true}}}
	if err := e.Validate(context.Background(), s); err == nil {
		t.Error("ssh accepted without provider")
	}
}

func TestEngineCancelKillsGroup(t *testing.T) {
	needRsync(t)
	e, root := newTestEngine(t)
	write(t, filepath.Join(root, "src", "big"), 50<<20)
	sink := &recSink{}
	x, err := e.Start(context.Background(), spec(root, engine.ModeCopy, false, &Options{V: 1, BwLimitKiB: i(1024)}), sink)
	if err != nil {
		t.Fatal(err)
	}
	pid := x.PID()
	time.Sleep(1500 * time.Millisecond)
	if p := x.Progress(); p.BytesDone == 0 {
		t.Errorf("no progress before cancel: %+v", p)
	}
	x.Cancel("test")
	x.Cancel("twice") // idempotent
	select {
	case <-x.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not terminate rsync")
	}
	res := x.Result()
	if res.Outcome != engine.OutcomeCancelled {
		t.Errorf("outcome %+v", res)
	}
	time.Sleep(200 * time.Millisecond)
	out, _ := exec.Command("pgrep", "-g", strconv.Itoa(pid)).Output()
	if s := strings.TrimSpace(string(out)); s != "" {
		t.Errorf("processes still alive in group %d: %s", pid, s)
	}
}

func TestEnginePartialCode23(t *testing.T) {
	needRsync(t)
	if os.Geteuid() == 0 {
		t.Skip("root can read everything")
	}
	e, root := newTestEngine(t)
	write(t, filepath.Join(root, "src", "ok"), 10)
	write(t, filepath.Join(root, "src", "secret"), 10)
	os.Chmod(filepath.Join(root, "src", "secret"), 0)
	defer os.Chmod(filepath.Join(root, "src", "secret"), 0o644)
	sink := &recSink{}
	x, err := e.Start(context.Background(), spec(root, engine.ModeCopy, false, &Options{V: 1}), sink)
	if err != nil {
		t.Fatal(err)
	}
	<-x.Done()
	res := x.Result()
	if res.Outcome != engine.OutcomePartial || *res.ExitCode != 23 || res.Final.Errors == 0 {
		t.Errorf("result %+v", res)
	}
}
