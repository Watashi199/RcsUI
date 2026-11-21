package store

import (
	"context"
	"time"
)

// Session est une session de connexion (le token brut n'est jamais stocké).
type Session struct {
	TokenHash []byte
	UserID    int64
	CSRFToken string
	CreatedAt time.Time
	LastSeen  time.Time
	ExpiresAt time.Time
	IP        string
	UserAgent string
}

// CreateSession insère une session.
func (db *DB) CreateSession(ctx context.Context, s Session) error {
	_, err := db.W.ExecContext(ctx, `INSERT INTO sessions(token_hash, user_id, csrf_token, created_at, last_seen_at,
		expires_at, ip, user_agent) VALUES (?,?,?,?,?,?,?,?)`,
		s.TokenHash, s.UserID, s.CSRFToken, s.CreatedAt.UnixMilli(), s.LastSeen.UnixMilli(), s.ExpiresAt.UnixMilli(),
		s.IP, s.UserAgent)
	return mapErr(err)
}

// GetSession retourne une session et son utilisateur.
func (db *DB) GetSession(ctx context.Context, hash []byte) (Session, User, error) {
	var s Session
	var created, seen, exp int64
	row := db.R.QueryRowContext(ctx, `SELECT s.token_hash, s.user_id, s.csrf_token, s.created_at, s.last_seen_at,
		s.expires_at, COALESCE(s.ip,''), COALESCE(s.user_agent,'') FROM sessions s WHERE s.token_hash=?`, hash)
	if err := row.Scan(&s.TokenHash, &s.UserID, &s.CSRFToken, &created, &seen, &exp, &s.IP, &s.UserAgent); err != nil {
		return Session{}, User{}, mapErr(err)
	}
	s.CreatedAt, s.LastSeen, s.ExpiresAt = fromMs(created), fromMs(seen), fromMs(exp)
	u, err := db.GetUser(ctx, s.UserID)
	return s, u, err
}

// TouchSession met à jour last_seen_at.
func (db *DB) TouchSession(ctx context.Context, hash []byte, t time.Time) error {
	_, err := db.W.ExecContext(ctx, `UPDATE sessions SET last_seen_at=? WHERE token_hash=?`, t.UnixMilli(), hash)
	return err
}

// DeleteSession supprime une session.
func (db *DB) DeleteSession(ctx context.Context, hash []byte) error {
	_, err := db.W.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, hash)
	return err
}

// DeleteUserSessions supprime toutes les sessions d'un utilisateur sauf `except` (peut être nil).
func (db *DB) DeleteUserSessions(ctx context.Context, userID int64, except []byte) error {
	if except == nil {
		except = []byte{}
	}
	_, err := db.W.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=? AND token_hash<>?`, userID, except)
	return err
}

// PurgeSessions supprime les sessions expirées ou inactives.
func (db *DB) PurgeSessions(ctx context.Context, now time.Time, idle time.Duration) (int64, error) {
	res, err := db.W.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at<? OR last_seen_at<?`,
		now.UnixMilli(), now.Add(-idle).UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
