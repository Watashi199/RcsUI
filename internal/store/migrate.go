package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type migration struct {
	version  int
	name     string
	sql      string
	checksum string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		name := e.Name()
		num, _, ok := strings.Cut(name, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || !strings.HasSuffix(name, ".sql") {
			return nil, fmt.Errorf("bad migration file name %q", name)
		}
		b, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		out = append(out, migration{version: v, name: name, sql: string(b), checksum: hex.EncodeToString(sum[:])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration versions must be contiguous from 1, got %d at position %d", m.version, i+1)
		}
	}
	return out, nil
}

// migrate applique les migrations en attente (forward-only). Avant toute
// migration sur une base existante, une copie est faite via VACUUM INTO.
func migrate(ctx context.Context, db *sql.DB, backupDir string) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, checksum TEXT NOT NULL, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	applied := map[int]string{}
	rows, err := db.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v int
		var c string
		if err := rows.Scan(&v, &c); err != nil {
			rows.Close()
			return err
		}
		applied[v] = c
	}
	rows.Close()

	var pending []migration
	for _, m := range ms {
		if c, ok := applied[m.version]; ok {
			if c != m.checksum {
				return fmt.Errorf("migration %s was modified after being applied (checksum mismatch)", m.name)
			}
			continue
		}
		pending = append(pending, m)
	}
	for v := range applied {
		if v > len(ms) {
			return fmt.Errorf("database schema version %d is newer than this binary (%d)", v, len(ms))
		}
	}
	if len(pending) == 0 {
		return nil
	}
	if len(applied) > 0 && backupDir != "" {
		if err := backup(ctx, db, backupDir, len(applied)); err != nil {
			return fmt.Errorf("pre-migration backup: %w", err)
		}
	}
	// Connexion épinglée : PRAGMA foreign_keys est propre à une connexion et
	// sans effet à l'intérieur d'une transaction.
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	for _, m := range pending {
		if err := applyMigration(ctx, conn, m); err != nil {
			return err
		}
	}
	return nil
}

// fkOffMarker, en tête d'une migration, la fait exécuter clés étrangères
// désactivées (recréation de table : sinon le DROP déclencherait les actions
// ON DELETE des tables qui la référencent). L'intégrité est vérifiée par
// PRAGMA foreign_key_check avant le commit.
const fkOffMarker = "-- rcsui:foreign-keys-off"

func applyMigration(ctx context.Context, conn *sql.Conn, m migration) (err error) {
	fkOff := strings.HasPrefix(m.sql, fkOffMarker)
	if fkOff {
		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
			return err
		}
		defer func() {
			if _, e := conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`); e != nil && err == nil {
				err = e
			}
		}()
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("migration %s: %w", m.name, err)
	}
	if fkOff {
		rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
		if err != nil {
			return fmt.Errorf("migration %s: foreign_key_check: %w", m.name, err)
		}
		bad := rows.Next()
		rows.Close()
		if bad {
			return fmt.Errorf("migration %s: foreign key violations after migration", m.name)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, checksum, applied_at) VALUES (?,?,?)`,
		m.version, m.checksum, now()); err != nil {
		return err
	}
	return tx.Commit()
}

func backup(ctx context.Context, db *sql.DB, dir string, version int) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	name := filepath.Join(dir, fmt.Sprintf("rcsui-v%d-%s.db", version, time.Now().UTC().Format("20060102T150405Z")))
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, name); err != nil {
		return err
	}
	// Ne conserver que les 5 dernières sauvegardes.
	matches, _ := filepath.Glob(filepath.Join(dir, "rcsui-v*.db"))
	sort.Strings(matches)
	for len(matches) > 5 {
		os.Remove(matches[0])
		matches = matches[1:]
	}
	return nil
}
