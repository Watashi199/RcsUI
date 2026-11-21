package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// SSHIdentity est une clé SSH gérée par l'application. La clé privée est
// stockée chiffrée (PrivateEnc) et n'est jamais sérialisée en JSON.
type SSHIdentity struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	KeyType     string    `json:"keyType"`
	PublicKey   string    `json:"publicKey"`
	PrivateEnc  []byte    `json:"-"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"createdAt"`
}

// KnownHost est une clé d'hôte SSH approuvée.
type KnownHost struct {
	ID          int64     `json:"id"`
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	KeyType     string    `json:"keyType"`
	PublicKey   string    `json:"publicKey"`
	Fingerprint string    `json:"fingerprint"`
	AddedBy     *int64    `json:"addedBy,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

// CreateSSHIdentity insère une identité ; private est appelé avec l'id pour
// chiffrer la clé avec des données associées liées à cet id.
func (db *DB) CreateSSHIdentity(ctx context.Context, id SSHIdentity, seal func(id int64) ([]byte, error)) (int64, error) {
	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO ssh_identities(name, key_type, public_key, private_enc, fingerprint, created_at)
		VALUES (?,?,?,?,?,?)`, id.Name, id.KeyType, id.PublicKey, []byte{}, id.Fingerprint, now())
	if err != nil {
		return 0, mapErr(err)
	}
	newID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	enc, err := seal(newID)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ssh_identities SET private_enc=? WHERE id=?`, enc, newID); err != nil {
		return 0, err
	}
	return newID, tx.Commit()
}

const identityCols = `id, name, key_type, public_key, private_enc, fingerprint, created_at`

func scanIdentity(s scanner) (SSHIdentity, error) {
	var i SSHIdentity
	var created int64
	if err := s.Scan(&i.ID, &i.Name, &i.KeyType, &i.PublicKey, &i.PrivateEnc, &i.Fingerprint, &created); err != nil {
		return SSHIdentity{}, mapErr(err)
	}
	i.CreatedAt = fromMs(created)
	return i, nil
}

// GetSSHIdentity retourne une identité (avec la clé chiffrée).
func (db *DB) GetSSHIdentity(ctx context.Context, id int64) (SSHIdentity, error) {
	return scanIdentity(db.R.QueryRowContext(ctx, `SELECT `+identityCols+` FROM ssh_identities WHERE id=?`, id))
}

// ListSSHIdentities liste les identités.
func (db *DB) ListSSHIdentities(ctx context.Context) ([]SSHIdentity, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT `+identityCols+` FROM ssh_identities ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SSHIdentity{}
	for rows.Next() {
		i, err := scanIdentity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// DeleteSSHIdentity supprime une identité (refusé par la base si un job l'utilise : ON DELETE RESTRICT).
func (db *DB) DeleteSSHIdentity(ctx context.Context, id int64) error {
	err := db.execOne(ctx, `DELETE FROM ssh_identities WHERE id=?`, id)
	if err != nil && isFK(err) {
		return ErrConflict
	}
	return err
}

func isFK(err error) bool {
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}

// ListIdentityGrants retourne les identités accordées à un utilisateur.
func (db *DB) ListIdentityGrants(ctx context.Context, userID int64) (map[int64]bool, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT identity_id FROM ssh_identity_grants WHERE user_id=?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// SetIdentityGrants remplace les utilisateurs autorisés sur une identité.
func (db *DB) SetIdentityGrants(ctx context.Context, identityID int64, userIDs []int64) error {
	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM ssh_identity_grants WHERE identity_id=?`, identityID); err != nil {
		return err
	}
	for _, u := range userIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO ssh_identity_grants(identity_id, user_id) VALUES (?,?)`, identityID, u); err != nil {
			return mapErr(err)
		}
	}
	return tx.Commit()
}

// ListIdentityGrantUsers liste les utilisateurs autorisés sur une identité.
func (db *DB) ListIdentityGrantUsers(ctx context.Context, identityID int64) ([]int64, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT user_id FROM ssh_identity_grants WHERE identity_id=? ORDER BY user_id`, identityID)
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

// AddKnownHost ajoute (ou remplace pour le même type) une clé d'hôte.
func (db *DB) AddKnownHost(ctx context.Context, h KnownHost) (int64, error) {
	res, err := db.W.ExecContext(ctx, `INSERT INTO ssh_known_hosts(host, port, key_type, public_key, fingerprint, added_by, created_at)
		VALUES (?,?,?,?,?,?,?) ON CONFLICT(host, port, key_type) DO UPDATE SET public_key=excluded.public_key,
		fingerprint=excluded.fingerprint, added_by=excluded.added_by, created_at=excluded.created_at`,
		h.Host, h.Port, h.KeyType, h.PublicKey, h.Fingerprint, nullInt(h.AddedBy), now())
	if err != nil {
		return 0, mapErr(err)
	}
	return res.LastInsertId()
}

// ListKnownHosts liste les clés d'hôte approuvées.
func (db *DB) ListKnownHosts(ctx context.Context) ([]KnownHost, error) {
	rows, err := db.R.QueryContext(ctx, `SELECT id, host, port, key_type, public_key, fingerprint, added_by, created_at
		FROM ssh_known_hosts ORDER BY host, port, key_type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KnownHost{}
	for rows.Next() {
		var h KnownHost
		var by sql.NullInt64
		var created int64
		if err := rows.Scan(&h.ID, &h.Host, &h.Port, &h.KeyType, &h.PublicKey, &h.Fingerprint, &by, &created); err != nil {
			return nil, err
		}
		h.AddedBy, h.CreatedAt = ptrInt(by), fromMs(created)
		out = append(out, h)
	}
	return out, rows.Err()
}

// DeleteKnownHost supprime une clé d'hôte.
func (db *DB) DeleteKnownHost(ctx context.Context, id int64) error {
	return db.execOne(ctx, `DELETE FROM ssh_known_hosts WHERE id=?`, id)
}
