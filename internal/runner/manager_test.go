package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"rcsui/internal/engine"
	"rcsui/internal/events"
	"rcsui/internal/store"
)

// fakeEngine produit des exécutions pilotées par le test.
type fakeEngine struct {
	mu       sync.Mutex
	startErr error
	invalid  bool
	ch       chan *fakeExec
}

func (f *fakeEngine) Kind() engine.Kind                   { return engine.KindRsync }
func (f *fakeEngine) DecodeOptions(b []byte) (any, error) { return string(b), nil }
func (f *fakeEngine) Validate(context.Context, engine.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.invalid {
		return &engine.ValidationError{Fields: map[string]string{"source.path": "outside roots"}}
	}
	return nil
}
func (f *fakeEngine) Preview(engine.Spec) (engine.Preview, error) { return engine.Preview{}, nil }
func (f *fakeEngine) Start(_ context.Context, s engine.Spec, sink engine.Sink) (engine.Execution, error) {
	f.mu.Lock()
	err := f.startErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	x := &fakeExec{spec: s, sink: sink, done: make(chan struct{}), cancelled: make(chan string, 1)}
	f.ch <- x
	return x, nil
}

type fakeExec struct {
	spec      engine.Spec
	sink      engine.Sink
	done      chan struct{}
	res       engine.Result
	cancelled chan string
	once      sync.Once
	mu        sync.Mutex
	ignore    bool // simule un moteur qui n'obéit pas à l'annulation
}

func (x *fakeExec) Progress() engine.Progress { return engine.Progress{} }
func (x *fakeExec) Cancel(reason string) {
	select {
	case x.cancelled <- reason:
	default:
	}
	x.mu.Lock()
	ign := x.ignore
	x.mu.Unlock()
	if !ign {
		x.finish(engine.Result{Outcome: engine.OutcomeCancelled})
	}
}
func (x *fakeExec) Done() <-chan struct{} { return x.done }
func (x *fakeExec) Result() engine.Result { return x.res }
func (x *fakeExec) PID() int              { return 4242 }
func (x *fakeExec) ExternalID() int64     { return 0 }
func (x *fakeExec) finish(r engine.Result) {
	x.once.Do(func() { x.res = r; close(x.done) })
}

type env struct {
	db  *store.DB
	m   *Manager
	eng *fakeEngine
	b   *events.Broker
	dir string
}

func newEnv(t *testing.T, max int) *env {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(context.Background(), filepath.Join(dir, "t.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	eng := &fakeEngine{ch: make(chan *fakeExec, 16)}
	b := events.NewBroker()
	m := New(Deps{DB: db, Engines: map[engine.Kind]engine.Engine{engine.KindRsync: eng}, Broker: b,
		LogDir: filepath.Join(dir, "logs"), PersistInterval: time.Millisecond, CancelTimeout: 300 * time.Millisecond}, max)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		m.Shutdown(ctx)
		db.Close()
	})
	return &env{db: db, m: m, eng: eng, b: b, dir: dir}
}

func (e *env) job(t *testing.T, name string) int64 {
	id, err := e.db.CreateJob(context.Background(), store.Job{Name: name, Engine: engine.KindRsync, Mode: engine.ModeCopy,
		Source:      engine.Endpoint{Type: engine.EndpointLocal, Path: "/a"},
		Destination: engine.Endpoint{Type: engine.EndpointLocal, Path: "/b"}, Options: json.RawMessage(`{"v":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) nextStart(t *testing.T) *fakeExec {
	t.Helper()
	select {
	case x := <-e.eng.ch:
		return x
	case <-time.After(3 * time.Second):
		t.Fatal("no run started")
		return nil
	}
}

func (e *env) noStart(t *testing.T) {
	t.Helper()
	select {
	case x := <-e.eng.ch:
		t.Fatalf("unexpected start of %s", x.spec.RunID)
	case <-time.After(150 * time.Millisecond):
	}
}

func waitState(t *testing.T, db *store.DB, id string, want store.RunState) store.Run {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		r, err := db.GetRun(context.Background(), id)
		if err == nil && r.State == want {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s: state %q, want %q (err %v)", id, r.State, want, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSuccessLifecycleAndLogs(t *testing.T) {
	e := newEnv(t, 2)
	ctx := context.Background()
	sub, _, _ := e.b.Subscribe("", "")
	jid := e.job(t, "j")
	id, err := e.m.Submit(ctx, jid, store.TriggerManual, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	x := e.nextStart(t)
	waitState(t, e.db, id, store.StateRunning)
	x.sink.OnLog(engine.LogLine{Stream: "stdout", Level: engine.LevelInfo, Msg: "hello"})
	x.sink.OnItem(engine.Item{Op: engine.ItemTransfer, Path: "a"})
	x.sink.OnProgress(engine.Progress{BytesDone: 5, BytesTotal: 10})
	if l, ok := e.m.Snapshot(id); !ok || l.Progress.BytesDone != 5 {
		t.Errorf("live=%+v %v", l, ok)
	}
	code := 0
	x.finish(engine.Result{Outcome: engine.OutcomeSucceeded, ExitCode: &code, Final: engine.Progress{BytesDone: 10, BytesTotal: 10, FilesDone: 1}})
	r := waitState(t, e.db, id, store.StateSucceeded)
	time.Sleep(50 * time.Millisecond) // laisser finish() terminer après la transition
	r, _ = e.db.GetRun(ctx, id)
	if r.BytesDone != 10 || r.FinishedAt == nil || r.StartedAt == nil || *r.PID != 4242 || r.LogSize == 0 {
		t.Errorf("run=%+v", r)
	}
	b, _ := os.ReadFile(filepath.Join(e.dir, "logs", r.LogPath))
	if !strings.Contains(string(b), `"msg":"hello"`) || !strings.Contains(string(b), "run finished: succeeded") {
		t.Errorf("log file: %s", b)
	}
	if e.m.IsJobActive(jid) {
		t.Error("job still active")
	}
	var states []string
	timeout := time.After(time.Second)
	for len(states) < 4 {
		select {
		case ev := <-sub.C:
			if ev.Type == events.TypeRunState {
				states = append(states, string(ev.Data.(StateEvent).To))
			}
		case <-timeout:
			t.Fatalf("states=%v", states)
		}
	}
	if strings.Join(states, ",") != "queued,starting,running,succeeded" {
		t.Errorf("states=%v", states)
	}
}

func TestConcurrencyLimitAndDuplicate(t *testing.T) {
	e := newEnv(t, 1)
	ctx := context.Background()
	j1, j2 := e.job(t, "j1"), e.job(t, "j2")
	id1, _ := e.m.Submit(ctx, j1, store.TriggerManual, nil, false)
	if _, err := e.m.Submit(ctx, j1, store.TriggerManual, nil, false); !errors.Is(err, ErrJobActive) {
		t.Fatalf("duplicate: %v", err)
	}
	x1 := e.nextStart(t)
	id2, err := e.m.Submit(ctx, j2, store.TriggerManual, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	e.noStart(t)
	waitState(t, e.db, id2, store.StateQueued)
	x1.finish(engine.Result{Outcome: engine.OutcomeSucceeded})
	waitState(t, e.db, id1, store.StateSucceeded)
	x2 := e.nextStart(t)
	if x2.spec.RunID != id2 {
		t.Errorf("FIFO violated")
	}
	e.m.SetMaxConcurrent(3)
	j3 := e.job(t, "j3")
	e.m.Submit(ctx, j3, store.TriggerManual, nil, false)
	e.nextStart(t)
}

func TestCancelQueuedAndRunning(t *testing.T) {
	e := newEnv(t, 1)
	ctx := context.Background()
	j1, j2 := e.job(t, "j1"), e.job(t, "j2")
	id1, _ := e.m.Submit(ctx, j1, store.TriggerManual, nil, false)
	x1 := e.nextStart(t)
	id2, _ := e.m.Submit(ctx, j2, store.TriggerManual, nil, false)
	if err := e.m.Cancel(id2); err != nil {
		t.Fatal(err)
	}
	waitState(t, e.db, id2, store.StateCancelled)
	waitState(t, e.db, id1, store.StateRunning)
	if err := e.m.Cancel(id1); err != nil {
		t.Fatal(err)
	}
	if reason := <-x1.cancelled; reason != "requested by user" {
		t.Errorf("reason=%q", reason)
	}
	waitState(t, e.db, id1, store.StateCancelled)
	time.Sleep(50 * time.Millisecond)
	if err := e.m.Cancel(id1); !errors.Is(err, ErrNotActive) {
		t.Errorf("cancel terminal: %v", err)
	}
	e.noStart(t)
}

func TestCancelNotConfirmed(t *testing.T) {
	e := newEnv(t, 1)
	jid := e.job(t, "j")
	id, _ := e.m.Submit(context.Background(), jid, store.TriggerManual, nil, false)
	x := e.nextStart(t)
	x.mu.Lock()
	x.ignore = true
	x.mu.Unlock()
	waitState(t, e.db, id, store.StateRunning)
	e.m.Cancel(id)
	r := waitState(t, e.db, id, store.StateFailed)
	if r.ErrorMessage != "cancellation not confirmed" {
		t.Errorf("msg=%q", r.ErrorMessage)
	}
	x.finish(engine.Result{Outcome: engine.OutcomeCancelled})
}

func TestStartFailureAndValidation(t *testing.T) {
	e := newEnv(t, 1)
	ctx := context.Background()
	jid := e.job(t, "j")
	e.eng.mu.Lock()
	e.eng.invalid = true
	e.eng.mu.Unlock()
	if _, err := e.m.Submit(ctx, jid, store.TriggerManual, nil, false); err == nil {
		t.Fatal("invalid spec accepted")
	}
	e.eng.mu.Lock()
	e.eng.invalid = false
	e.eng.startErr = errors.New("exec: rsync not found")
	e.eng.mu.Unlock()
	id, err := e.m.Submit(ctx, jid, store.TriggerManual, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	r := waitState(t, e.db, id, store.StateFailed)
	if !strings.Contains(r.ErrorMessage, "rsync not found") {
		t.Errorf("msg=%q", r.ErrorMessage)
	}
	time.Sleep(50 * time.Millisecond)
	if e.m.IsJobActive(jid) {
		t.Error("job still active after start failure")
	}
}

func TestRecover(t *testing.T) {
	e := newEnv(t, 1)
	ctx := context.Background()
	jid := e.job(t, "j")
	j, _ := e.db.GetJob(ctx, jid)
	j.RetryInterrupted = true
	e.db.UpdateJob(ctx, j)
	if err := e.db.InsertRun(ctx, store.Run{ID: "01OLD", JobID: &jid, JobName: "j", Engine: "rsync", Trigger: store.TriggerManual,
		State: store.StateQueued, SpecJSON: "{}", QueuedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	p := 777
	now := time.Now()
	e.db.TransitionRun(ctx, "01OLD", store.StateQueued, store.StateRunning, store.RunPatch{PID: &p, StartedAt: &now})
	var killed []int
	n, err := e.m.Recover(ctx, func(pid int, runID string) (bool, error) { killed = append(killed, pid); return true, nil })
	if err != nil || n != 1 {
		t.Fatalf("recover n=%d err=%v", n, err)
	}
	r := waitState(t, e.db, "01OLD", store.StateInterrupted)
	if r.ErrorMessage != "server restarted" || len(killed) != 1 || killed[0] != 777 {
		t.Errorf("run=%+v killed=%v", r, killed)
	}
	x := e.nextStart(t)
	if x.spec.RunID == "01OLD" {
		t.Error("old run restarted instead of new")
	}
}

func TestShutdown(t *testing.T) {
	e := newEnv(t, 1)
	ctx := context.Background()
	j1, j2 := e.job(t, "j1"), e.job(t, "j2")
	id1, _ := e.m.Submit(ctx, j1, store.TriggerManual, nil, false)
	e.nextStart(t)
	waitState(t, e.db, id1, store.StateRunning)
	id2, _ := e.m.Submit(ctx, j2, store.TriggerManual, nil, false)
	sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := e.m.Shutdown(sctx); err != nil {
		t.Fatal(err)
	}
	waitState(t, e.db, id1, store.StateInterrupted)
	waitState(t, e.db, id2, store.StateInterrupted)
	if _, err := e.m.Submit(ctx, j1, store.TriggerManual, nil, false); !errors.Is(err, ErrShuttingDown) {
		t.Errorf("submit after shutdown: %v", err)
	}
}

func TestRecordSkippedAndRetry(t *testing.T) {
	e := newEnv(t, 1)
	ctx := context.Background()
	jid := e.job(t, "j")
	j, _ := e.db.GetJob(ctx, jid)
	id, _ := e.m.Submit(ctx, jid, store.TriggerManual, nil, true)
	x := e.nextStart(t)
	if !x.spec.DryRun {
		t.Error("dry-run flag lost")
	}
	if err := e.m.RecordSkipped(ctx, j, "previous run still active"); err != nil {
		t.Fatal(err)
	}
	x.finish(engine.Result{Outcome: engine.OutcomePartial})
	waitState(t, e.db, id, store.StatePartial)
	time.Sleep(50 * time.Millisecond)
	id2, err := e.m.Retry(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	x2 := e.nextStart(t)
	if x2.spec.RunID != id2 || !x2.spec.DryRun {
		t.Errorf("retry spec %+v", x2.spec)
	}
	r, _ := e.db.GetRun(ctx, id2)
	if r.Trigger != store.TriggerRetry {
		t.Errorf("trigger=%q", r.Trigger)
	}
}

func TestNewIDSortable(t *testing.T) {
	a := NewID(time.UnixMilli(1000))
	b := NewID(time.UnixMilli(2000))
	if len(a) != 26 || a >= b {
		t.Errorf("a=%s b=%s", a, b)
	}
	if NewID(time.UnixMilli(1000)) == a {
		t.Error("ids not random")
	}
}
