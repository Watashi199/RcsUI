package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"rcsui/internal/engine"
)

// RunState est l'état d'une exécution (PLAN.md §4.3).
type RunState string

const (
	StateQueued      RunState = "queued"
	StateStarting    RunState = "starting"
	StateRunning     RunState = "running"
	StateCancelling  RunState = "cancelling"
	StateSucceeded   RunState = "succeeded"
	StatePartial     RunState = "partial"
	StateFailed      RunState = "failed"
	StateCancelled   RunState = "cancelled"
	StateInterrupted RunState = "interrupted"
	StateSkipped     RunState = "skipped"
)

// ActiveStates sont les états non terminaux.
var ActiveStates = []RunState{StateQueued, StateStarting, StateRunning, StateCancelling}

// Terminal indique si l'état est terminal.
func (s RunState) Terminal() bool {
	switch s {
	case StateQueued, StateStarting, StateRunning, StateCancelling:
		return false
	}
	return true
}

// Trigger est l'origine d'un run.
type Trigger string

const (
	TriggerManual   Trigger = "manual"
	TriggerSchedule Trigger = "schedule"
	TriggerRetry    Trigger = "retry"
)

// ErrStateConflict signale qu'une transition a échoué car l'état courant a changé.
var ErrStateConflict = errors.New("run state changed concurrently")

// Run est une exécution.
type Run struct {
	ID           string     `json:"id"`
	JobID        *int64     `json:"jobId,omitempty"`
	JobName      string     `json:"jobName"`
	Engine       string     `json:"engine"`
	Trigger      Trigger    `json:"trigger"`
	TriggeredBy  *int64     `json:"triggeredBy,omitempty"`
	DryRun       bool       `json:"dryRun"`
	Attempt      int        `json:"attempt"`
	State        RunState   `json:"state"`
	SpecJSON     string     `json:"-"`
	QueuedAt     time.Time  `json:"queuedAt"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
	ExitCode     *int64     `json:"exitCode,omitempty"`
	RcloneJobID  *int64     `json:"-"`
	PID          *int64     `json:"-"`
	ErrorMessage string     `json:"error,omitempty"`
	BytesDone    int64      `json:"bytesDone"`
	BytesTotal   *int64     `json:"bytesTotal,omitempty"`
	FilesDone    int64      `json:"filesDone"`
	FilesTotal   *int64     `json:"filesTotal,omitempty"`
	FilesDeleted int64      `json:"filesDeleted"`
	ErrorsCount  int64      `json:"errors"`
	LogPath      string     `json:"-"`
	LogSize      int64      `json:"logSize"`
}

const runCols = `id, job_id, job_name, engine, trigger, triggered_by, dry_run, state, spec_json, queued_at, started_at,
	finished_at, exit_code, rclone_job_id, pid, COALESCE(error_message,''), bytes_done, bytes_total, files_done,
	files_total, files_deleted, errors_count, COALESCE(log_path,''), log_size, attempt`

func scanRun(s scanner) (Run, error) {
	var r Run
	var jobID, by, started, finished, exit, rcl, pid, bt, ft sql.NullInt64
	var queued int64
	err := s.Scan(&r.ID, &jobID, &r.JobName, &r.Engine, &r.Trigger, &by, &r.DryRun, &r.State, &r.SpecJSON, &queued,
		&started, &finished, &exit, &rcl, &pid, &r.ErrorMessage, &r.BytesDone, &bt, &r.FilesDone, &ft,
		&r.FilesDeleted, &r.ErrorsCount, &r.LogPath, &r.LogSize, &r.Attempt)
	if err != nil {
		return Run{}, mapErr(err)
	}
	r.JobID, r.TriggeredBy, r.ExitCode, r.RcloneJobID, r.PID = ptrInt(jobID), ptrInt(by), ptrInt(exit), ptrInt(rcl), ptrInt(pid)
	r.BytesTotal, r.FilesTotal = ptrInt(bt), ptrInt(ft)
	r.QueuedAt = fromMs(queued)
	r.StartedAt, r.FinishedAt = fromNullMs(started), fromNullMs(finished)
	return r, nil
}

// InsertRun insère un run. Retourne ErrConflict si un run actif existe déjà
// pour ce job (index unique partiel runs_one_active_per_job).
func (db *DB) InsertRun(ctx context.Context, r Run) error {
	_, err := db.W.ExecContext(ctx, `INSERT INTO runs(id, job_id, job_name, engine, trigger, triggered_by, dry_run,
		state, spec_json, queued_at, finished_at, error_message, log_path, attempt)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, nullInt(r.JobID), r.JobName, r.Engine, r.Trigger, nullInt(r.TriggeredBy), b2i(r.DryRun), r.State,
		r.SpecJSON, r.QueuedAt.UnixMilli(), nullTime(r.FinishedAt), nullStr(r.ErrorMessage), nullStr(r.LogPath), max(r.Attempt, 1))
	return mapErr(err)
}

// RunPatch décrit les champs mis à jour lors d'une transition.
type RunPatch struct {
	StartedAt    *time.Time
	FinishedAt   *time.Time
	ExitCode     *int
	RcloneJobID  *int64
	PID          *int
	ErrorMessage *string
	Progress     *engine.Progress
	LogSize      *int64
}

func (p RunPatch) apply(set []string, args []any) ([]string, []any) {
	if p.StartedAt != nil {
		set, args = append(set, "started_at=?"), append(args, p.StartedAt.UnixMilli())
	}
	if p.FinishedAt != nil {
		set, args = append(set, "finished_at=?"), append(args, p.FinishedAt.UnixMilli())
	}
	if p.ExitCode != nil {
		set, args = append(set, "exit_code=?"), append(args, *p.ExitCode)
	}
	if p.RcloneJobID != nil {
		set, args = append(set, "rclone_job_id=?"), append(args, *p.RcloneJobID)
	}
	if p.PID != nil {
		set, args = append(set, "pid=?"), append(args, *p.PID)
	}
	if p.ErrorMessage != nil {
		set, args = append(set, "error_message=?"), append(args, nullStr(*p.ErrorMessage))
	}
	if p.LogSize != nil {
		set, args = append(set, "log_size=?"), append(args, *p.LogSize)
	}
	if pr := p.Progress; pr != nil {
		set = append(set, "bytes_done=?", "bytes_total=?", "files_done=?", "files_total=?", "files_deleted=?", "errors_count=?")
		args = append(args, pr.BytesDone, nullPos(pr.BytesTotal), pr.FilesDone, nullPos(pr.FilesTotal), pr.Deletes, pr.Errors)
	}
	return set, args
}

// TransitionRun passe un run de `from` à `to` (concurrence optimiste) en
// appliquant le patch. Retourne ErrStateConflict si l'état n'est plus `from`.
func (db *DB) TransitionRun(ctx context.Context, id string, from, to RunState, p RunPatch) error {
	set, args := p.apply([]string{"state=?"}, []any{to})
	res, err := db.W.ExecContext(ctx, `UPDATE runs SET `+join(set)+` WHERE id=? AND state=?`, append(args, id, from)...)
	if err != nil {
		return mapErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrStateConflict
	}
	return nil
}

// UpdateRun applique un patch sans changer d'état (progression périodique).
func (db *DB) UpdateRun(ctx context.Context, id string, p RunPatch) error {
	set, args := p.apply(nil, nil)
	if len(set) == 0 {
		return nil
	}
	_, err := db.W.ExecContext(ctx, `UPDATE runs SET `+join(set)+` WHERE id=?`, append(args, id)...)
	return err
}

// GetRun retourne un run.
func (db *DB) GetRun(ctx context.Context, id string) (Run, error) {
	return scanRun(db.R.QueryRowContext(ctx, `SELECT `+runCols+` FROM runs WHERE id=?`, id))
}

// RunFilter filtre la liste des runs.
type RunFilter struct {
	JobID  *int64
	JobIDs []int64 // restriction d'autorisation (nil = pas de restriction)
	States []RunState
	Before string // curseur : id de run (ULID, ordre chronologique)
	Limit  int
}

// ListRuns liste les runs, du plus récent au plus ancien.
func (db *DB) ListRuns(ctx context.Context, f RunFilter) ([]Run, error) {
	var where []string
	var args []any
	if f.JobID != nil {
		where, args = append(where, "job_id=?"), append(args, *f.JobID)
	}
	if f.JobIDs != nil {
		if len(f.JobIDs) == 0 {
			return []Run{}, nil
		}
		where = append(where, "job_id IN ("+placeholders(len(f.JobIDs))+")")
		for _, id := range f.JobIDs {
			args = append(args, id)
		}
	}
	if len(f.States) > 0 {
		where = append(where, "state IN ("+placeholders(len(f.States))+")")
		for _, s := range f.States {
			args = append(args, s)
		}
	}
	if f.Before != "" {
		where, args = append(where, "id<?"), append(args, f.Before)
	}
	q := `SELECT ` + runCols + ` FROM runs`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	q += ` ORDER BY id DESC LIMIT ?`
	rows, err := db.R.QueryContext(ctx, q, append(args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LastRunsByJob retourne le dernier run de chaque job.
func (db *DB) LastRunsByJob(ctx context.Context) (map[int64]Run, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT `+runCols+` FROM runs WHERE id IN
		(SELECT MAX(id) FROM runs WHERE job_id IS NOT NULL GROUP BY job_id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		if r.JobID != nil {
			out[*r.JobID] = r
		}
	}
	return out, rows.Err()
}

// PurgeRuns supprime les runs terminés avant `before` et retourne leurs chemins de logs.
func (db *DB) PurgeRuns(ctx context.Context, before time.Time) ([]string, error) {
	rows, err := db.W.QueryContext(ctx, `DELETE FROM runs WHERE finished_at IS NOT NULL AND finished_at<? RETURNING COALESCE(log_path,'')`,
		before.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		if p != "" {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}

// LastScheduledRunAt retourne l'heure de mise en file du dernier run déclenché
// par le scheduler pour ce job (zéro si aucun).
func (db *DB) LastScheduledRunAt(ctx context.Context, jobID int64) (time.Time, error) {
	var ms sql.NullInt64
	err := db.R.QueryRowContext(ctx, `SELECT MAX(queued_at) FROM runs WHERE job_id=? AND trigger='schedule'`, jobID).Scan(&ms)
	if err != nil || !ms.Valid {
		return time.Time{}, err
	}
	return fromMs(ms.Int64), nil
}

// LastSuccessByJob retourne, par job, l'heure de fin du dernier run réel réussi.
func (db *DB) LastSuccessByJob(ctx context.Context) (map[int64]time.Time, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT job_id, MAX(finished_at) FROM runs
		WHERE job_id IS NOT NULL AND state='succeeded' AND dry_run=0 AND finished_at IS NOT NULL GROUP BY job_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]time.Time{}
	for rows.Next() {
		var id, ms int64
		if err := rows.Scan(&id, &ms); err != nil {
			return nil, err
		}
		out[id] = fromMs(ms)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func nullTime(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}

func nullPos(v int64) sql.NullInt64 {
	if v <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: v, Valid: true}
}
