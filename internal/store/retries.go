package store

import (
	"context"
	"time"
)

// PendingRetry est une relance automatique programmée (M7, persistée depuis M9.1).
type PendingRetry struct {
	JobID   int64
	At      time.Time
	Attempt int
	Resync  bool
}

// SavePendingRetry enregistre (ou remplace) la relance en attente d'un job.
func (db *DB) SavePendingRetry(ctx context.Context, p PendingRetry) error {
	_, err := db.W.ExecContext(ctx, `INSERT INTO pending_retries(job_id, retry_at, attempt, resync) VALUES (?,?,?,?)
		ON CONFLICT(job_id) DO UPDATE SET retry_at=excluded.retry_at, attempt=excluded.attempt, resync=excluded.resync`,
		p.JobID, p.At.UnixMilli(), p.Attempt, b2i(p.Resync))
	return err
}

// DeletePendingRetry supprime la relance en attente d'un job (sans erreur si absente).
func (db *DB) DeletePendingRetry(ctx context.Context, jobID int64) error {
	_, err := db.W.ExecContext(ctx, `DELETE FROM pending_retries WHERE job_id=?`, jobID)
	return err
}

// ListPendingRetries retourne les relances en attente, la plus proche en premier.
func (db *DB) ListPendingRetries(ctx context.Context) ([]PendingRetry, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT job_id, retry_at, attempt, resync FROM pending_retries ORDER BY retry_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingRetry
	for rows.Next() {
		var p PendingRetry
		var at int64
		var resync int
		if err := rows.Scan(&p.JobID, &at, &p.Attempt, &resync); err != nil {
			return nil, err
		}
		p.At, p.Resync = fromMs(at), resync != 0
		out = append(out, p)
	}
	return out, rows.Err()
}
