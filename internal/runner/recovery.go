package runner

import (
	"context"
	"time"

	"rcsui/internal/store"
)

// OrphanKiller tue un processus orphelin d'un run précédent. Injecté pour les tests.
type OrphanKiller func(pid int, runID string) (killed bool, err error)

// Recover marque "interrupted" les runs restés actifs lors d'un arrêt brutal,
// tue les éventuels processus rsync orphelins, puis relance les jobs qui ont
// retry_interrupted. À appeler avant d'ouvrir le HTTP et le scheduler.
func (m *Manager) Recover(ctx context.Context, kill OrphanKiller) (int, error) {
	runs, err := m.d.DB.ListRuns(ctx, store.RunFilter{States: store.ActiveStates, Limit: 500})
	if err != nil {
		return 0, err
	}
	if kill == nil {
		kill = killOrphan
	}
	var retry []int64
	for _, r := range runs {
		if r.PID != nil && *r.PID > 0 {
			if killed, err := kill(int(*r.PID), r.ID); err != nil {
				m.d.Log.Warn("orphan check", "run", r.ID, "pid", *r.PID, "err", err)
			} else if killed {
				m.d.Log.Warn("killed orphan rsync process group", "run", r.ID, "pid", *r.PID)
			}
		}
		now := time.Now()
		msg := "server restarted"
		if err := m.d.DB.TransitionRun(ctx, r.ID, r.State, store.StateInterrupted,
			store.RunPatch{FinishedAt: &now, ErrorMessage: &msg}); err != nil {
			m.d.Log.Error("mark interrupted", "run", r.ID, "err", err)
			continue
		}
		if r.JobID != nil && !r.DryRun {
			if j, err := m.d.DB.GetJob(ctx, *r.JobID); err == nil && j.RetryInterrupted {
				retry = append(retry, j.ID)
			}
		}
	}
	for _, id := range retry {
		if _, err := m.Submit(ctx, id, store.TriggerRetry, nil, false); err != nil {
			m.d.Log.Warn("retry interrupted job", "job", id, "err", err)
		}
	}
	return len(runs), nil
}
