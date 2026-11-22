package runner

import (
	"context"
	"time"

	"rcsui/internal/engine"
	"rcsui/internal/store"
)

// Relances automatiques (PLAN.md §11, M7) : un run réel qui échoue avec
// Result.Retryable (erreur réseau/protocole rsync, erreur non fatale rclone) est
// relancé après job.RetryDelaySec, tant que attempt <= job.RetryMax. La
// relance relit la définition courante du job. Les timers vivent en mémoire :
// un redémarrage les perd (retry_interrupted couvre le cas des runs coupés).

// retryUnit convertit RetryDelaySec en durée (remplaçable dans les tests).
func (m *Manager) retryUnit() time.Duration {
	if m.d.RetryUnit > 0 {
		return m.d.RetryUnit
	}
	return time.Second
}

// scheduleRetry programme une relance si le job l'autorise. Retourne l'heure prévue.
func (m *Manager) scheduleRetry(ar *activeRun) *time.Time {
	if ar.run.DryRun {
		return nil
	}
	j, err := m.d.DB.GetJob(context.Background(), ar.jobID)
	if err != nil || j.RetryMax < ar.attempt {
		return nil
	}
	delay := time.Duration(max(j.RetryDelaySec, 1)) * m.retryUnit()
	next := ar.attempt + 1
	jid := ar.jobID
	at := time.Now().Add(delay)

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return nil
	}
	m.clearRetryLocked(jid)
	p := &pendingRetry{at: at, resync: ar.spec.Resync}
	p.timer = time.AfterFunc(delay, func() { m.fireRetry(jid, next, p) })
	m.retries[jid] = p
	m.d.Log.Info("automatic retry scheduled", "job", j.Name, "attempt", next, "of", j.RetryMax+1, "at", at.Format(time.RFC3339))
	return &at
}

func (m *Manager) fireRetry(jobID int64, attempt int, p *pendingRetry) {
	m.mu.Lock()
	if m.closing || m.retries[jobID] != p {
		m.mu.Unlock()
		return
	}
	delete(m.retries, jobID)
	m.mu.Unlock()

	ctx := context.Background()
	j, err := m.d.DB.GetJob(ctx, jobID)
	if err != nil {
		m.d.Log.Warn("automatic retry: job unavailable", "job", jobID, "err", err)
		return
	}
	snap := SnapshotOf(j)
	snap.Resync = p.resync // la relance d'un resync reste un resync
	if _, err := m.submitSnapshot(ctx, snap, store.TriggerRetry, nil, false, attempt); err != nil {
		m.d.Log.Warn("automatic retry not started", "job", j.Name, "attempt", attempt, "err", err)
	}
}

// clearRetryLocked annule la relance en attente d'un job. Appelé sous m.mu.
func (m *Manager) clearRetryLocked(jobID int64) {
	if p, ok := m.retries[jobID]; ok {
		p.timer.Stop()
		delete(m.retries, jobID)
	}
}

// CancelRetry annule la relance automatique en attente d'un job.
func (m *Manager) CancelRetry(jobID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.retries[jobID]
	m.clearRetryLocked(jobID)
	return ok
}

// PendingRetry retourne l'heure de la relance automatique en attente d'un job.
func (m *Manager) PendingRetry(jobID int64) *time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.retries[jobID]; ok {
		at := p.at
		return &at
	}
	return nil
}

func (m *Manager) runHooks(f Finished) {
	for _, h := range m.d.OnFinished {
		func() {
			defer func() {
				if r := recover(); r != nil {
					m.d.Log.Error("run hook panicked", "run", f.Run.ID, "panic", r)
				}
			}()
			h(f)
		}()
	}
}

// finalRun reconstitue l'état final d'un run pour les hooks.
func (ar *activeRun) finalRun(to store.RunState, at time.Time, msg string, code *int, p engine.Progress) store.Run {
	r := ar.run
	r.State = to
	r.FinishedAt = &at
	r.ErrorMessage = msg
	r.Attempt = max(ar.attempt, 1)
	if code != nil {
		c := int64(*code)
		r.ExitCode = &c
	}
	r.BytesDone, r.FilesDone, r.FilesDeleted, r.ErrorsCount = p.BytesDone, p.FilesDone, p.Deletes, p.Errors
	if p.BytesTotal > 0 {
		bt := p.BytesTotal
		r.BytesTotal = &bt
	}
	if p.FilesTotal > 0 {
		ft := p.FilesTotal
		r.FilesTotal = &ft
	}
	return r
}

// StateCounts retourne le nombre de runs actifs par état (métriques).
func (m *Manager) StateCounts() map[store.RunState]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[store.RunState]int{}
	for _, s := range store.ActiveStates {
		out[s] = 0
	}
	for _, ar := range m.active {
		out[ar.state]++
	}
	return out
}
