package store

import (
	"context"
	"database/sql"
	"time"
)

// Role d'un utilisateur.
type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
)

// Valid indique si le rôle est connu.
func (r Role) Valid() bool { return r == RoleAdmin || r == RoleOperator || r == RoleViewer }

// User est un compte.
type User struct {
	ID           int64      `json:"id"`
	Username     string     `json:"username"`
	PasswordHash string     `json:"-"`
	Role         Role       `json:"role"`
	OIDCIssuer   string     `json:"-"`
	OIDCSubject  string     `json:"-"`
	MustChangePW bool       `json:"mustChangePassword"`
	Disabled     bool       `json:"disabled"`
	CreatedAt    time.Time  `json:"createdAt"`
	LastLoginAt  *time.Time `json:"lastLoginAt,omitempty"`
}

const userCols = `id, username, COALESCE(password_hash,''), role, COALESCE(oidc_issuer,''), COALESCE(oidc_subject,''),
	must_change_pw, disabled, created_at, last_login_at`

type scanner interface{ Scan(...any) error }

func scanUser(s scanner) (User, error) {
	var u User
	var created int64
	var last sql.NullInt64
	err := s.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.OIDCIssuer, &u.OIDCSubject,
		&u.MustChangePW, &u.Disabled, &created, &last)
	u.CreatedAt = fromMs(created)
	u.LastLoginAt = fromNullMs(last)
	return u, mapErr(err)
}

// CountUsers compte les comptes.
func (db *DB) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := db.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateUser insère un utilisateur et retourne son id.
func (db *DB) CreateUser(ctx context.Context, u User) (int64, error) {
	t := now()
	res, err := db.W.ExecContext(ctx, `INSERT INTO users(username, password_hash, role, oidc_issuer, oidc_subject,
		must_change_pw, disabled, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		u.Username, nullStr(u.PasswordHash), u.Role, nullStr(u.OIDCIssuer), nullStr(u.OIDCSubject),
		b2i(u.MustChangePW), b2i(u.Disabled), t, t)
	if err != nil {
		return 0, mapErr(err)
	}
	return res.LastInsertId()
}

// GetUser retourne un utilisateur par id.
func (db *DB) GetUser(ctx context.Context, id int64) (User, error) {
	return scanUser(db.R.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=?`, id))
}

// GetUserByUsername retourne un utilisateur par nom (insensible à la casse).
func (db *DB) GetUserByUsername(ctx context.Context, name string) (User, error) {
	return scanUser(db.R.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username=?`, name))
}

// GetUserByOIDC retourne un utilisateur par (issuer, subject).
func (db *DB) GetUserByOIDC(ctx context.Context, iss, sub string) (User, error) {
	return scanUser(db.R.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE oidc_issuer=? AND oidc_subject=?`, iss, sub))
}

// ListUsers liste tous les utilisateurs.
func (db *DB) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UserPatch décrit une mise à jour partielle.
type UserPatch struct {
	PasswordHash *string
	Role         *Role
	MustChangePW *bool
	Disabled     *bool
}

// UpdateUser applique une mise à jour partielle.
func (db *DB) UpdateUser(ctx context.Context, id int64, p UserPatch) error {
	set, args := []string{"updated_at=?"}, []any{now()}
	if p.PasswordHash != nil {
		set, args = append(set, "password_hash=?"), append(args, *p.PasswordHash)
	}
	if p.Role != nil {
		set, args = append(set, "role=?"), append(args, *p.Role)
	}
	if p.MustChangePW != nil {
		set, args = append(set, "must_change_pw=?"), append(args, b2i(*p.MustChangePW))
	}
	if p.Disabled != nil {
		set, args = append(set, "disabled=?"), append(args, b2i(*p.Disabled))
	}
	return db.execOne(ctx, `UPDATE users SET `+join(set)+` WHERE id=?`, append(args, id)...)
}

// TouchLogin enregistre la date de dernière connexion.
func (db *DB) TouchLogin(ctx context.Context, id int64) error {
	_, err := db.W.ExecContext(ctx, `UPDATE users SET last_login_at=? WHERE id=?`, now(), id)
	return err
}

// DeleteUser supprime un utilisateur.
func (db *DB) DeleteUser(ctx context.Context, id int64) error {
	return db.execOne(ctx, `DELETE FROM users WHERE id=?`, id)
}

// CountActiveAdmins compte les administrateurs actifs (on interdit de supprimer le dernier).
func (db *DB) CountActiveAdmins(ctx context.Context) (int, error) {
	var n int
	err := db.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role='admin' AND disabled=0`).Scan(&n)
	return n, err
}

func (db *DB) execOne(ctx context.Context, q string, args ...any) error {
	res, err := db.W.ExecContext(ctx, q, args...)
	if err != nil {
		return mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func join(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
