package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// TestMigration0003KeepsReferences applique les migrations 1 et 2, insère un
// job référencé par un grant et un run, puis applique 0003 (recréation de
// job_definitions) : le grant (ON DELETE CASCADE) et le lien du run (ON DELETE
// SET NULL) doivent survivre, et le mode bisync doit être accepté.
func TestMigration0003KeepsReferences(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", dsn(filepath.Join(dir, "t.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY, checksum TEXT NOT NULL, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms[:2] {
		if err := applyMigration(ctx, conn, m); err != nil {
			t.Fatal(err)
		}
	}
	conn.Close()

	for _, q := range []string{
		`INSERT INTO users (id, username, role, created_at, updated_at) VALUES (1, 'op', 'operator', 1, 1)`,
		`INSERT INTO job_definitions (id, name, engine, mode, source_json, destination_json, owner_id, created_at, updated_at, retry_max)
		 VALUES (7, 'j', 'rclone', 'copy', '{}', '{}', 1, 1, 2, 3)`,
		`INSERT INTO job_grants (job_id, user_id, permission) VALUES (7, 1, 'run')`,
		`INSERT INTO runs (id, job_id, job_name, engine, trigger, state, spec_json, queued_at)
		 VALUES ('r1', 7, 'j', 'rclone', 'manual', 'succeeded', '{}', 1)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	if err := migrate(ctx, db, filepath.Join(dir, "backups")); err != nil {
		t.Fatal(err)
	}

	var grants, retryMax int
	var runJob sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_grants WHERE job_id = 7`).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT job_id FROM runs WHERE id = 'r1'`).Scan(&runJob); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT retry_max FROM job_definitions WHERE id = 7`).Scan(&retryMax); err != nil {
		t.Fatal(err)
	}
	if grants != 1 || !runJob.Valid || runJob.Int64 != 7 || retryMax != 3 {
		t.Fatalf("data lost: grants=%d runJob=%v retryMax=%d", grants, runJob, retryMax)
	}
	if _, err := db.ExecContext(ctx, `UPDATE job_definitions SET mode = 'bisync' WHERE id = 7`); err != nil {
		t.Fatalf("bisync mode rejected: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE job_definitions SET mode = 'nope' WHERE id = 7`); err == nil {
		t.Fatal("CHECK constraint on mode lost")
	}
	// Les clés étrangères sont réactivées : la suppression du job doit cascader.
	if _, err := db.ExecContext(ctx, `DELETE FROM job_definitions WHERE id = 7`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_grants`).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if grants != 0 {
		t.Fatal("foreign keys not re-enabled after migration")
	}
}
