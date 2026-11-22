package runner

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"rcsui/internal/engine"
	"rcsui/internal/store"
)

// Relances persistées (M9.1) : survie au redémarrage et fenêtre manuel/relance.

func (e *env) pendingRows(t *testing.T) []store.PendingRetry {
	t.Helper()
	list, err := e.db.ListPendingRetries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// failRetryable fait échouer le prochain run avec une erreur transitoire.
func (e *env) failRetryable(t *testing.T, jobID int64) string {
	t.Helper()
	id, err := e.m.Submit(context.Background(), jobID, store.TriggerManual, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	e.nextStart(t).finish(engine.Result{Outcome: engine.OutcomeFailed, Retryable: true, Err: errNet})
	return id
}

func TestRetryArmedBeforeJobIsReleased(t *testing.T) {
	e := newEnv(t, 2)
	e.m.d.RetryUnit = time.Hour
	jid := e.job(t, "j")
	e.setRetry(t, jid, 5, 1)
	for i := 0; i < 20; i++ {
		e.failRetryable(t, jid)
		// Dès que le job n'est plus actif, la relance doit exister : sinon un
		// lancement manuel arrivé à cet instant ne la remplacerait pas.
		deadline := time.Now().Add(3 * time.Second)
		for e.m.IsJobActive(jid) {
			if time.Now().After(deadline) {
				t.Fatal("job still active")
			}
			time.Sleep(time.Millisecond)
		}
		if e.m.PendingRetry(jid) == nil {
			t.Fatalf("iteration %d: job released before its retry was armed", i)
		}
		if !e.m.CancelRetry(jid) {
			t.Fatal("CancelRetry found nothing")
		}
		if rows := e.pendingRows(t); len(rows) != 0 {
			t.Fatalf("CancelRetry left %v in the database", rows)
		}
	}
}

func TestManualRunClearsPersistedRetry(t *testing.T) {
	e := newEnv(t, 2)
	e.m.d.RetryUnit = time.Hour
	jid := e.job(t, "j")
	e.setRetry(t, jid, 1, 1)
	id := e.failRetryable(t, jid)
	waitState(t, e.db, id, store.StateFailed)
	waitPendingRetry(t, e.m, jid)
	if rows := e.pendingRows(t); len(rows) != 1 || rows[0].Attempt != 2 || rows[0].JobID != jid {
		t.Fatalf("persisted retry = %+v", rows)
	}
	if _, err := e.m.Submit(context.Background(), jid, store.TriggerManual, nil, false); err != nil {
		t.Fatal(err)
	}
	if rows := e.pendingRows(t); len(rows) != 0 {
		t.Fatalf("manual run left %v in the database", rows)
	}
	e.nextStart(t).finish(engine.Result{Outcome: engine.OutcomeSucceeded})
}

func TestRetrySurvivesRestart(t *testing.T) {
	e := newEnv(t, 2)
	e.m.d.RetryUnit = time.Hour
	jid := e.job(t, "j")
	e.setRetry(t, jid, 1, 1)
	ctx := context.Background()
	id := e.failRetryable(t, jid)
	waitState(t, e.db, id, store.StateFailed)
	waitPendingRetry(t, e.m, jid)

	// Arrêt : le timer s'arrête, la relance reste en base.
	sctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := e.m.Shutdown(sctx); err != nil {
		t.Fatal(err)
	}
	rows := e.pendingRows(t)
	if len(rows) != 1 {
		t.Fatalf("shutdown dropped the retry: %v", rows)
	}

	// Redémarrage, l'heure prévue étant passée pendant l'arrêt : la relance part aussitôt.
	rows[0].At = time.Now().Add(-time.Minute)
	if err := e.db.SavePendingRetry(ctx, rows[0]); err != nil {
		t.Fatal(err)
	}
	m2 := New(Deps{DB: e.db, Engines: e.m.d.Engines, Broker: e.b, LogDir: filepath.Join(e.dir, "logs"),
		PersistInterval: time.Millisecond, CancelTimeout: 300 * time.Millisecond}, 2)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		m2.Shutdown(ctx)
	})
	n, err := m2.RestoreRetries(ctx)
	if err != nil || n != 1 {
		t.Fatalf("RestoreRetries = %d, %v", n, err)
	}
	x := e.nextStart(t)
	r, err := e.db.GetRun(ctx, x.spec.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Trigger != store.TriggerRetry || r.Attempt != 2 {
		t.Errorf("restored run: trigger=%s attempt=%d", r.Trigger, r.Attempt)
	}
	if rows := e.pendingRows(t); len(rows) != 0 {
		t.Errorf("consumed retry still persisted: %v", rows)
	}
	x.finish(engine.Result{Outcome: engine.OutcomeSucceeded})
}

func TestImpossibleRetryIsDropped(t *testing.T) {
	e := newEnv(t, 2)
	e.m.d.RetryUnit = time.Millisecond
	jid := e.job(t, "j")
	e.setRetry(t, jid, 1, 50)
	id := e.failRetryable(t, jid)
	waitState(t, e.db, id, store.StateFailed)
	// Le job devient invalide avant la relance : elle échoue à la validation et
	// ne doit pas repartir à chaque démarrage.
	e.eng.mu.Lock()
	e.eng.invalid = true
	e.eng.mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for len(e.pendingRows(t)) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("impossible retry still persisted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e.m.PendingRetry(jid) != nil {
		t.Error("impossible retry still pending in memory")
	}
}

func TestRetryFiringDuringShutdownIsKept(t *testing.T) {
	e := newEnv(t, 2)
	jid := e.job(t, "j")
	ctx := context.Background()
	p := store.PendingRetry{JobID: jid, At: time.Now().Add(time.Hour), Attempt: 2}
	if err := e.db.SavePendingRetry(ctx, p); err != nil {
		t.Fatal(err)
	}
	e.m.mu.Lock()
	e.m.armRetryLocked(p)
	pr := e.m.retries[jid]
	e.m.closing = true // arrêt en cours au moment où le timer part
	e.m.mu.Unlock()
	e.m.fireRetry(jid, 2, pr)
	if rows := e.pendingRows(t); len(rows) != 1 {
		t.Fatalf("retry lost during shutdown: %v", rows)
	}
}

func TestRestoreRetriesSkipsActiveJob(t *testing.T) {
	e := newEnv(t, 2)
	jid := e.job(t, "j")
	ctx := context.Background()
	if _, err := e.m.Submit(ctx, jid, store.TriggerManual, nil, false); err != nil {
		t.Fatal(err)
	}
	x := e.nextStart(t)
	// Relancé par retry_interrupted avant la restauration : le nouveau run remplace la relance.
	if err := e.db.SavePendingRetry(ctx, store.PendingRetry{JobID: jid, At: time.Now(), Attempt: 2}); err != nil {
		t.Fatal(err)
	}
	if n, err := e.m.RestoreRetries(ctx); err != nil || n != 0 {
		t.Fatalf("RestoreRetries = %d, %v", n, err)
	}
	if e.m.PendingRetry(jid) != nil || len(e.pendingRows(t)) != 0 {
		t.Error("retry kept for an active job")
	}
	x.finish(engine.Result{Outcome: engine.OutcomeSucceeded})
}

func TestPendingRetryDeletedWithJob(t *testing.T) {
	e := newEnv(t, 2)
	jid := e.job(t, "j")
	ctx := context.Background()
	if err := e.db.SavePendingRetry(ctx, store.PendingRetry{JobID: jid, At: time.Now(), Attempt: 2, Resync: true}); err != nil {
		t.Fatal(err)
	}
	if rows := e.pendingRows(t); len(rows) != 1 || !rows[0].Resync {
		t.Fatalf("rows = %+v", rows)
	}
	if err := e.db.DeleteJob(ctx, jid); err != nil {
		t.Fatal(err)
	}
	if rows := e.pendingRows(t); len(rows) != 0 {
		t.Errorf("retry survived its job: %v", rows)
	}
}
