package store

import (
	"context"
	"database/sql"
	"time"
)

// APIToken est un jeton d'API personnel (le jeton brut n'est jamais stocké).
type APIToken struct {
	ID         int64      `json:"id"`
	UserID     int64      `json:"userId"`
	Name       string     `json:"name"`
	TokenHash  []byte     `json:"-"`
	Prefix     string     `json:"prefix"` // début du jeton, pour le reconnaître
	Role       Role       `json:"role"`   // plafond des droits du jeton
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

const tokenCols = `id, user_id, name, token_hash, prefix, role, created_at, expires_at, last_used_at`

func scanToken(s scanner) (APIToken, error) {
	var t APIToken
	var created int64
	var exp, used sql.NullInt64
	if err := s.Scan(&t.ID, &t.UserID, &t.Name, &t.TokenHash, &t.Prefix, &t.Role, &created, &exp, &used); err != nil {
		return APIToken{}, mapErr(err)
	}
	t.CreatedAt = fromMs(created)
	t.ExpiresAt, t.LastUsedAt = timePtr(exp), timePtr(used)
	return t, nil
}

func timePtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMs(v.Int64)
	return &t
}

// MaxTokensPerUser borne le nombre de jetons d'un utilisateur.
const MaxTokensPerUser = 20

// ErrTooManyTokens : limite de jetons atteinte pour cet utilisateur.
var ErrTooManyTokens = errorString("too many API tokens")

type errorString string

func (e errorString) Error() string { return string(e) }

// CreateAPIToken insère un jeton (ErrConflict si le nom existe déjà pour cet
// utilisateur, ErrTooManyTokens si la limite est atteinte).
func (db *DB) CreateAPIToken(ctx context.Context, t APIToken) (int64, error) {
	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_tokens WHERE user_id=?`, t.UserID).Scan(&n); err != nil {
		return 0, err
	}
	if n >= MaxTokensPerUser {
		return 0, ErrTooManyTokens
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO api_tokens(user_id, name, token_hash, prefix, role, created_at, expires_at)
		VALUES (?,?,?,?,?,?,?)`, t.UserID, t.Name, t.TokenHash, t.Prefix, t.Role, t.CreatedAt.UnixMilli(), nullTime(t.ExpiresAt))
	if err != nil {
		return 0, mapErr(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// GetAPITokenByHash retourne un jeton et son utilisateur.
func (db *DB) GetAPITokenByHash(ctx context.Context, hash []byte) (APIToken, User, error) {
	t, err := scanToken(db.R.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE token_hash=?`, hash))
	if err != nil {
		return APIToken{}, User{}, err
	}
	u, err := db.GetUser(ctx, t.UserID)
	return t, u, err
}

// ListAPITokens liste les jetons d'un utilisateur.
func (db *DB) ListAPITokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE user_id=? ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteAPIToken révoque un jeton de l'utilisateur (ErrNotFound sinon).
func (db *DB) DeleteAPIToken(ctx context.Context, userID, id int64) error {
	return db.execOne(ctx, `DELETE FROM api_tokens WHERE id=? AND user_id=?`, id, userID)
}

// TouchAPIToken met à jour last_used_at.
func (db *DB) TouchAPIToken(ctx context.Context, id int64, t time.Time) error {
	_, err := db.W.ExecContext(ctx, `UPDATE api_tokens SET last_used_at=? WHERE id=?`, t.UnixMilli(), id)
	return err
}
