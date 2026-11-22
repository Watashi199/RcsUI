package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"rcsui/internal/engine"
)

// Job est une définition de job enregistrée.
type Job struct {
	ID               int64           `json:"id"`
	Name             string          `json:"name"`
	Engine           engine.Kind     `json:"engine"`
	Mode             engine.Mode     `json:"mode"`
	Source           engine.Endpoint `json:"source"`
	Destination      engine.Endpoint `json:"destination"`
	Options          json.RawMessage `json:"options"`
	ScheduleCron     string          `json:"scheduleCron,omitempty"`
	ScheduleOn       bool            `json:"scheduleEnabled"`
	RetryInterrupted bool            `json:"retryInterrupted"`
	RetryMax         int             `json:"retryMax"`
	RetryDelaySec    int             `json:"retryDelaySec"`
	CatchUp          bool            `json:"catchUp"`
	// ExclusiveDest : ne pas démarrer si un autre job actif écrit au même endroit.
	ExclusiveDest bool      `json:"exclusiveDestination"`
	OwnerID       *int64    `json:"ownerId,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

const jobCols = `id, name, engine, mode, source_json, destination_json, options_json, COALESCE(schedule_cron,''),
	schedule_on, retry_interrupted, retry_max, retry_delay_s, catch_up, exclusive_dest, owner_id, created_at, updated_at`

func scanJob(s scanner) (Job, error) {
	var j Job
	var src, dst, opts string
	var owner sql.NullInt64
	var created, updated int64
	if err := s.Scan(&j.ID, &j.Name, &j.Engine, &j.Mode, &src, &dst, &opts, &j.ScheduleCron, &j.ScheduleOn,
		&j.RetryInterrupted, &j.RetryMax, &j.RetryDelaySec, &j.CatchUp, &j.ExclusiveDest, &owner, &created, &updated); err != nil {
		return Job{}, mapErr(err)
	}
	if err := json.Unmarshal([]byte(src), &j.Source); err != nil {
		return Job{}, err
	}
	if err := json.Unmarshal([]byte(dst), &j.Destination); err != nil {
		return Job{}, err
	}
	j.Options = json.RawMessage(opts)
	j.OwnerID = ptrInt(owner)
	j.CreatedAt, j.UpdatedAt = fromMs(created), fromMs(updated)
	return j, nil
}

func jobArgs(j Job) ([]any, error) {
	src, err := json.Marshal(j.Source)
	if err != nil {
		return nil, err
	}
	dst, err := json.Marshal(j.Destination)
	if err != nil {
		return nil, err
	}
	opts := string(j.Options)
	if opts == "" {
		opts = "{}"
	}
	var sshID sql.NullInt64
	for _, ep := range []engine.Endpoint{j.Source, j.Destination} {
		if ep.SSH != nil && ep.SSH.Identity.ManagedID != nil {
			sshID = sql.NullInt64{Int64: *ep.SSH.Identity.ManagedID, Valid: true}
		}
	}
	return []any{j.Name, j.Engine, j.Mode, string(src), string(dst), opts, sshID, nullStr(j.ScheduleCron),
		b2i(j.ScheduleOn), b2i(j.RetryInterrupted), j.RetryMax, j.RetryDelaySec, b2i(j.CatchUp), b2i(j.ExclusiveDest)}, nil
}

// CreateJob insère un job et retourne son id.
func (db *DB) CreateJob(ctx context.Context, j Job) (int64, error) {
	args, err := jobArgs(j)
	if err != nil {
		return 0, err
	}
	t := now()
	res, err := db.W.ExecContext(ctx, `INSERT INTO job_definitions(name, engine, mode, source_json, destination_json,
		options_json, ssh_identity_id, schedule_cron, schedule_on, retry_interrupted, retry_max, retry_delay_s, catch_up,
		exclusive_dest, owner_id, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, append(args, nullInt(j.OwnerID), t, t)...)
	if err != nil {
		return 0, mapErr(err)
	}
	return res.LastInsertId()
}

// UpdateJob remplace un job.
func (db *DB) UpdateJob(ctx context.Context, j Job) error {
	args, err := jobArgs(j)
	if err != nil {
		return err
	}
	return db.execOne(ctx, `UPDATE job_definitions SET name=?, engine=?, mode=?, source_json=?, destination_json=?,
		options_json=?, ssh_identity_id=?, schedule_cron=?, schedule_on=?, retry_interrupted=?, retry_max=?, retry_delay_s=?,
		catch_up=?, exclusive_dest=?, updated_at=? WHERE id=?`,
		append(args, now(), j.ID)...)
}

// GetJob retourne un job.
func (db *DB) GetJob(ctx context.Context, id int64) (Job, error) {
	return scanJob(db.R.QueryRowContext(ctx, `SELECT `+jobCols+` FROM job_definitions WHERE id=?`, id))
}

// ListJobs retourne tous les jobs triés par nom.
func (db *DB) ListJobs(ctx context.Context) ([]Job, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT `+jobCols+` FROM job_definitions ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// DeleteJob supprime un job (les runs restent, job_id passe à NULL).
func (db *DB) DeleteJob(ctx context.Context, id int64) error {
	return db.execOne(ctx, `DELETE FROM job_definitions WHERE id=?`, id)
}

// JobPermission est un droit sur un job.
type JobPermission string

const (
	PermView JobPermission = "view"
	PermRun  JobPermission = "run"
	PermEdit JobPermission = "edit"
)

// Level ordonne les permissions (view < run < edit).
func (p JobPermission) Level() int {
	switch p {
	case PermView:
		return 1
	case PermRun:
		return 2
	case PermEdit:
		return 3
	}
	return 0
}

// JobGrant associe un utilisateur et une permission sur un job.
type JobGrant struct {
	UserID     int64         `json:"userId"`
	Permission JobPermission `json:"permission"`
}

// GetJobPermission retourne la permission d'un utilisateur sur un job ("" si aucune).
func (db *DB) GetJobPermission(ctx context.Context, jobID, userID int64) (JobPermission, error) {
	var p JobPermission
	err := db.R.QueryRowContext(ctx, `SELECT permission FROM job_grants WHERE job_id=? AND user_id=?`, jobID, userID).Scan(&p)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return p, err
}

// ListUserJobPermissions retourne les permissions d'un utilisateur, par job.
func (db *DB) ListUserJobPermissions(ctx context.Context, userID int64) (map[int64]JobPermission, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT job_id, permission FROM job_grants WHERE user_id=?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]JobPermission{}
	for rows.Next() {
		var id int64
		var p JobPermission
		if err := rows.Scan(&id, &p); err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, rows.Err()
}

// ListJobGrants liste les droits d'un job.
func (db *DB) ListJobGrants(ctx context.Context, jobID int64) ([]JobGrant, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT user_id, permission FROM job_grants WHERE job_id=? ORDER BY user_id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []JobGrant{}
	for rows.Next() {
		var g JobGrant
		if err := rows.Scan(&g.UserID, &g.Permission); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SetJobGrants remplace les droits d'un job.
func (db *DB) SetJobGrants(ctx context.Context, jobID int64, grants []JobGrant) error {
	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM job_grants WHERE job_id=?`, jobID); err != nil {
		return err
	}
	for _, g := range grants {
		if _, err := tx.ExecContext(ctx, `INSERT INTO job_grants(job_id, user_id, permission) VALUES (?,?,?)`,
			jobID, g.UserID, g.Permission); err != nil {
			return mapErr(err)
		}
	}
	return tx.Commit()
}

// SetJobGrant crée ou remplace un droit.
func (db *DB) SetJobGrant(ctx context.Context, jobID int64, g JobGrant) error {
	_, err := db.W.ExecContext(ctx, `INSERT INTO job_grants(job_id, user_id, permission) VALUES (?,?,?)
		ON CONFLICT(job_id, user_id) DO UPDATE SET permission=excluded.permission`, jobID, g.UserID, g.Permission)
	return mapErr(err)
}

// ListRemoteGrants retourne les remotes accordés à un utilisateur.
func (db *DB) ListRemoteGrants(ctx context.Context, userID int64) (map[string]bool, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT remote_name FROM remote_grants WHERE user_id=?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

// SetRemoteGrants remplace les utilisateurs autorisés sur un remote.
func (db *DB) SetRemoteGrants(ctx context.Context, remote string, userIDs []int64) error {
	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM remote_grants WHERE remote_name=?`, remote); err != nil {
		return err
	}
	for _, u := range userIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO remote_grants(remote_name, user_id) VALUES (?,?)`, remote, u); err != nil {
			return mapErr(err)
		}
	}
	return tx.Commit()
}

// ListRemoteGrantUsers liste les utilisateurs autorisés sur un remote.
func (db *DB) ListRemoteGrantUsers(ctx context.Context, remote string) ([]int64, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT user_id FROM remote_grants WHERE remote_name=? ORDER BY user_id`, remote)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// JobsUsingRemote retourne les noms des jobs qui utilisent un remote.
func (db *DB) JobsUsingRemote(ctx context.Context, remote string) ([]string, error) {
	jobs, err := db.ListJobs(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, j := range jobs {
		if j.Source.Remote == remote || j.Destination.Remote == remote {
			out = append(out, j.Name)
		}
	}
	return out, nil
}
