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
// relance relit la définition courante du job.
//
// Depuis M9.1, la relance est aussi écrite dans pending_retries : elle survit à
// un redémarrage (RestoreRetries). Un nouveau run du job (manuel, planifié ou la
// relance elle-même) la remplace, en mémoire comme en base.

// retryUnit convertit RetryDelaySec en durée (remplaçable dans les tests).
func (m *Manager) retryUnit() time.Duration {
	if m.d.RetryUnit > 0 {
		return m.d.RetryUnit
	}
	return time.Second
}

// planRetry décide si un run en échec doit être relancé et enregistre la
// relance en base. Appelé hors verrou, AVANT la transition finale : le run est
// encore actif, aucun autre run du job ne peut démarrer entre-temps.
func (m *Manager) planRetry(ar *activeRun) *store.PendingRetry {
	if ar.run.DryRun {
		return nil
	}
	ctx := context.Background()
	j, err := m.d.DB.GetJob(ctx, ar.jobID)
	if err != nil || j.RetryMax < ar.attempt {
		return nil
	}
	delay := time.Duration(max(j.RetryDelaySec, 1)) * m.retryUnit()
	p := &store.PendingRetry{JobID: j.ID, At: time.Now().Add(delay), Attempt: ar.attempt + 1, Resync: ar.spec.Resync}
	if err := m.d.DB.SavePendingRetry(ctx, *p); err != nil {
		// La relance reste programmée en mémoire ; seul le redémarrage la perdrait.
		m.d.Log.Error("persist pending retry", "job", j.Name, "err", err)
	}
	m.d.Log.Info("automatic retry scheduled", "job", j.Name, "attempt", p.Attempt, "of", j.RetryMax+1, "at", p.At.Format(time.RFC3339))
	return p
}

// armRetryLocked programme le timer d'une relance. Appelé sous m.mu, dans la
// même section critique que la libération du run (finish) : un lancement
// manuel voit soit le run actif (409), soit la relance (qu'il remplace).
// Pendant l'arrêt, rien n'est armé : la relance reste en base pour le prochain démarrage.
func (m *Manager) armRetryLocked(p store.PendingRetry) *time.Time {
	if m.closing {
		return nil
	}
	m.stopRetryLocked(p.JobID)
	pr := &pendingRetry{at: p.At, resync: p.Resync}
	jid, attempt := p.JobID, p.Attempt
	pr.timer = time.AfterFunc(max(time.Until(p.At), 0), func() { m.fireRetry(jid, attempt, pr) })
	m.retries[jid] = pr
	at := p.At
	return &at
}

// RestoreRetries réarme les relances enregistrées avant un redémarrage. À appeler
// après Recover : un job relancé par retry_interrupted n'a plus de relance en attente.
// Une relance dont l'heure est passée part immédiatement.
func (m *Manager) RestoreRetries(ctx context.Context) (int, error) {
	list, err := m.d.DB.ListPendingRetries(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range list {
		if _, err := m.d.DB.GetJob(ctx, p.JobID); err != nil {
			m.d.Log.Warn("pending retry dropped: job unavailable", "job", p.JobID, "err", err)
			_ = m.d.DB.DeletePendingRetry(ctx, p.JobID)
			continue
		}
		m.mu.Lock()
		if _, busy := m.byJob[p.JobID]; busy {
			m.clearRetryLocked(p.JobID)
		} else if m.armRetryLocked(p) != nil {
			n++
		}
		m.mu.Unlock()
	}
	return n, nil
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
	// Consommée, qu'elle démarre ou non (sinon elle repartirait au prochain démarrage).
	if err := m.d.DB.DeletePendingRetry(ctx, jobID); err != nil {
		m.d.Log.Error("delete pending retry", "job", jobID, "err", err)
	}
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

// stopRetryLocked arrête le timer d'une relance sans toucher à la base. Appelé sous m.mu.
func (m *Manager) stopRetryLocked(jobID int64) {
	if p, ok := m.retries[jobID]; ok {
		p.timer.Stop()
		delete(m.retries, jobID)
	}
}

// clearRetryLocked annule la relance en attente d'un job, en mémoire et en base. Appelé sous m.mu.
func (m *Manager) clearRetryLocked(jobID int64) {
	m.stopRetryLocked(jobID)
	if err := m.d.DB.DeletePendingRetry(context.Background(), jobID); err != nil {
		m.d.Log.Error("delete pending retry", "job", jobID, "err", err)
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
