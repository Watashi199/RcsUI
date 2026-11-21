// Package store est la couche de persistance SQLite (modernc.org/sqlite, sans CGO).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // driver "sqlite"
)

// ErrNotFound est retournée quand une ligne n'existe pas.
var ErrNotFound = errors.New("not found")

// ErrConflict est retournée sur violation de contrainte d'unicité.
var ErrConflict = errors.New("conflict")

// DB regroupe un pool d'écriture (1 connexion : écritures sérialisées) et un
// pool de lecture.
type DB struct {
	W *sql.DB
	R *sql.DB
}

func dsn(path string) string {
	p := filepath.ToSlash(path)
	return "file:" + p + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
}

// Open ouvre la base et applique les migrations en attente.
func Open(ctx context.Context, path string, backupDir string) (*DB, error) {
	w, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	r, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(4)
	db := &DB{W: w, R: r}
	if err := w.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := migrate(ctx, w, backupDir); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Close ferme les deux pools.
func (db *DB) Close() error {
	return errors.Join(db.W.Close(), db.R.Close())
}

// now retourne l'horodatage courant en millisecondes UTC.
func now() int64 { return time.Now().UnixMilli() }

func fromMs(v int64) time.Time { return time.UnixMilli(v).UTC() }

func fromNullMs(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMs(v.Int64)
	return &t
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case isUnique(err):
		return fmt.Errorf("%w: %v", ErrConflict, err)
	default:
		return err
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullInt(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

func ptrInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	x := v.Int64
	return &x
}
