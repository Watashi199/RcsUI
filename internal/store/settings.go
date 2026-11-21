package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"
)

// GetSetting retourne une valeur ("" et false si absente).
func (db *DB) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := db.R.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return v, err == nil, err
}

// SetSetting écrit une valeur.
func (db *DB) SetSetting(ctx context.Context, key, value string) error {
	_, err := db.W.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES (?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// GetIntSetting lit un entier avec valeur par défaut.
func (db *DB) GetIntSetting(ctx context.Context, key string, def int) int {
	v, ok, err := db.GetSetting(ctx, key)
	if err != nil || !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// GetBoolSetting lit un booléen avec valeur par défaut.
func (db *DB) GetBoolSetting(ctx context.Context, key string, def bool) bool {
	v, ok, err := db.GetSetting(ctx, key)
	if err != nil || !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

// AuditEntry est une ligne du journal d'audit.
type AuditEntry struct {
	ID      int64          `json:"id"`
	At      time.Time      `json:"at"`
	UserID  *int64         `json:"userId,omitempty"`
	Action  string         `json:"action"`
	Target  string         `json:"target,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// AddAudit ajoute une entrée d'audit. Ne jamais y mettre de secret.
func (db *DB) AddAudit(ctx context.Context, userID *int64, action, target string, details map[string]any) error {
	var d sql.NullString
	if len(details) > 0 {
		b, err := json.Marshal(details)
		if err != nil {
			return err
		}
		d = sql.NullString{String: string(b), Valid: true}
	}
	_, err := db.W.ExecContext(ctx, `INSERT INTO audit_log(at, user_id, action, target, details) VALUES (?,?,?,?,?)`,
		now(), nullInt(userID), action, nullStr(target), d)
	return err
}

// ListAudit liste le journal, du plus récent au plus ancien.
func (db *DB) ListAudit(ctx context.Context, beforeID int64, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if beforeID <= 0 {
		beforeID = 1 << 62
	}
	rows, err := db.R.QueryContext(ctx, `SELECT id, at, user_id, action, COALESCE(target,''), COALESCE(details,'')
		FROM audit_log WHERE id<? ORDER BY id DESC LIMIT ?`, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var at int64
		var uid sql.NullInt64
		var details string
		if err := rows.Scan(&e.ID, &at, &uid, &e.Action, &e.Target, &details); err != nil {
			return nil, err
		}
		e.At, e.UserID = fromMs(at), ptrInt(uid)
		if details != "" {
			_ = json.Unmarshal([]byte(details), &e.Details)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
