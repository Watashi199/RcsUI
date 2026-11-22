package runner

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"rcsui/internal/engine"
	"rcsui/internal/store"
)

type hookRec struct {
	mu  sync.Mutex
	got []Finished
}

func (h *hookRec) add(f Finished) { h.mu.Lock(); h.got = append(h.got, f); h.mu.Unlock() }
func (h *hookRec) all() []Finished {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Finished(nil), h.got...)
}

func (e *env) setRetry(t *testing.T, jobID int64, maxRetries, delay int) {
	t.Helper()
	ctx := context.Background()
	j, err := e.db.GetJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	j.RetryMax, j.RetryDelaySec = maxRetries, delay
	if err := e.db.UpdateJob(ctx, j); err != nil {
		t.Fatal(err)
	}
}

var errNet = errors.New("connection reset")

func TestAutomaticRetry(t *testing.T) {
	e := newEnv(t, 2)
	rec := &hookRec{}
	e.m.d.RetryUnit = time.Millisecond
	e.m.d.OnFinished = []func(Finished){rec.add}
	jid := e.job(t, "flaky")
	e.setRetry(t, jid, 2, 20)
	ctx := context.Background()

	id1, err := e.m.Submit(ctx, jid, store.TriggerManual, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	// Tentatives 1 et 2 : échec transitoire, relance automatique.
	e.nextStart(t).finish(engine.Result{Outcome: engine.OutcomeFailed, Retryable: true, Err: errNet})
	waitState(t, e.db, id1, store.StateFailed)
	x2 := e.nextStart(t)
	r2, err := e.db.GetRun(ctx, x2.spec.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Trigger != store.TriggerRetry || r2.Attempt != 2 {
		t.Fatalf("second run: trigger=%s attempt=%d", r2.Trigger, r2.Attempt)
	}
	x2.finish(engine.Result{Outcome: engine.OutcomeFailed, Retryable: true, Err: errNet})
	x3 := e.nextStart(t)
	x3.finish(engine.Result{Outcome: engine.OutcomeFailed, Retryable: true, Err: errNet})
	waitState(t, e.db, x3.spec.RunID, store.StateFailed)
	// retry_max=2 : pas de 4e tentative.
	e.noStart(t)
	if at := e.m.PendingRetry(jid); at != nil {
		t.Fatalf("unexpected pending retry at %v", at)
	}

	got := rec.all()
	if len(got) != 3 {
		t.Fatalf("hooks: got %d calls, want 3", len(got))
	}
	if got[0].RetryAt == nil || got[1].RetryAt == nil || got[2].RetryAt != nil {
		t.Fatalf("retryAt: %v %v %v", got[0].RetryAt, got[1].RetryAt, got[2].RetryAt)
	}
	if got[2].Run.Attempt != 3 || got[2].Run.State != store.StateFailed || got[2].Run.ErrorMessage == "" {
		t.Fatalf("final hook run: %+v", got[2].Run)
	}
}

func TestNoRetryWhenNotRetryableOrDryRun(t *testing.T) {
	e := newEnv(t, 2)
	e.m.d.RetryUnit = time.Millisecond
	jid := e.job(t, "j")
	e.setRetry(t, jid, 3, 1)
	ctx := context.Background()

	id, _ := e.m.Submit(ctx, jid, store.TriggerManual, nil, false)
	e.nextStart(t).finish(engine.Result{Outcome: engine.OutcomeFailed, Err: errors.New("permission denied")})
	waitState(t, e.db, id, store.StateFailed)
	e.noStart(t)

	id, _ = e.m.Submit(ctx, jid, store.TriggerManual, nil, true)
	e.nextStart(t).finish(engine.Result{Outcome: engine.OutcomeFailed, Retryable: true, Err: errNet})
	waitState(t, e.db, id, store.StateFailed)
	e.noStart(t)
}

func waitPendingRetry(t *testing.T, m *Manager, jobID int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for m.PendingRetry(jobID) == nil {
		if time.Now().After(deadline) {
			t.Fatal("retry should be pending")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestManualRunReplacesPendingRetry(t *testing.T) {
	e := newEnv(t, 2)
	e.m.d.RetryUnit = time.Hour // la relance ne doit jamais partir d'elle-même
	jid := e.job(t, "j")
	e.setRetry(t, jid, 1, 1)
	ctx := context.Background()

	id, _ := e.m.Submit(ctx, jid, store.TriggerManual, nil, false)
	e.nextStart(t).finish(engine.Result{Outcome: engine.OutcomeFailed, Retryable: true, Err: errNet})
	waitState(t, e.db, id, store.StateFailed)
	// finish écrit l'état final avant de programmer la relance : on attend celle-ci.
	waitPendingRetry(t, e.m, jid)
	if _, err := e.m.Submit(ctx, jid, store.TriggerManual, nil, false); err != nil {
		t.Fatal(err)
	}
	if e.m.PendingRetry(jid) != nil {
		t.Fatal("manual run should clear the pending retry")
	}
	e.nextStart(t).finish(engine.Result{Outcome: engine.OutcomeSucceeded})
}
