package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rcsui/internal/engine"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(context.Background(), filepath.Join(dir, "t.db"), filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMigrateIdempotentAndChecksum(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.db")
	ctx := context.Background()
	db, err := Open(ctx, p, filepath.Join(dir, "b"))
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(ctx, p, filepath.Join(dir, "b"))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "b")); len(entries) != 0 {
		t.Errorf("unexpected backups: %v", entries)
	}
	// Une migration modifiée après application doit être détectée.
	if _, err := db.W.Exec(`UPDATE schema_migrations SET checksum='x' WHERE version=1`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(ctx, p, ""); err == nil {
		t.Error("checksum mismatch not detected")
	}
}

func TestUsersAndSessions(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	id, err := db.CreateUser(ctx, User{Username: "Admin", PasswordHash: "h", Role: RoleAdmin, MustChangePW: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateUser(ctx, User{Username: "admin", Role: RoleViewer}); !errors.Is(err, ErrConflict) {
		t.Errorf("case-insensitive unique: %v", err)
	}
	u, err := db.GetUserByUsername(ctx, "ADMIN")
	if err != nil || u.ID != id || !u.MustChangePW || u.Role != RoleAdmin {
		t.Fatalf("get: %+v %v", u, err)
	}
	f := false
	if err := db.UpdateUser(ctx, id, UserPatch{MustChangePW: &f}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateUser(ctx, 999, UserPatch{MustChangePW: &f}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing: %v", err)
	}
	now := time.Now()
	s := Session{TokenHash: []byte("h1"), UserID: id, CSRFToken: "c", CreatedAt: now, LastSeen: now, ExpiresAt: now.Add(time.Hour)}
	if err := db.CreateSession(ctx, s); err != nil {
		t.Fatal(err)
	}
	s.TokenHash = []byte("h2")
	db.CreateSession(ctx, s)
	gs, gu, err := db.GetSession(ctx, []byte("h1"))
	if err != nil || gs.CSRFToken != "c" || gu.ID != id {
		t.Fatalf("session: %+v %+v %v", gs, gu, err)
	}
	db.DeleteUserSessions(ctx, id, []byte("h1"))
	if _, _, err := db.GetSession(ctx, []byte("h2")); !errors.Is(err, ErrNotFound) {
		t.Errorf("other session not deleted: %v", err)
	}
	if _, _, err := db.GetSession(ctx, []byte("h1")); err != nil {
		t.Errorf("kept session deleted: %v", err)
	}
	n, _ := db.PurgeSessions(ctx, now.Add(2*time.Hour), 24*time.Hour)
	if n != 1 {
		t.Errorf("purge=%d", n)
	}
}

func newJob(t *testing.T, db *DB, name string) int64 {
	id, err := db.CreateJob(context.Background(), Job{Name: name, Engine: engine.KindRsync, Mode: engine.ModeCopy,
		Source:      engine.Endpoint{Type: engine.EndpointLocal, Path: "/a"},
		Destination: engine.Endpoint{Type: engine.EndpointRemote, Remote: "b2", Path: "x"},
		Options:     []byte(`{"v":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestJobs(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	id := newJob(t, db, "j1")
	j, err := db.GetJob(ctx, id)
	if err != nil || j.Destination.Remote != "b2" || string(j.Options) != `{"v":1}` {
		t.Fatalf("%+v %v", j, err)
	}
	j.ScheduleCron, j.ScheduleOn = "0 3 * * *", true
	if err := db.UpdateJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	names, _ := db.JobsUsingRemote(ctx, "b2")
	if len(names) != 1 {
		t.Errorf("JobsUsingRemote=%v", names)
	}
	uid, _ := db.CreateUser(ctx, User{Username: "op", Role: RoleOperator})
	db.SetJobGrant(ctx, id, JobGrant{UserID: uid, Permission: PermRun})
	db.SetJobGrant(ctx, id, JobGrant{UserID: uid, Permission: PermEdit})
	p, _ := db.GetJobPermission(ctx, id, uid)
	if p != PermEdit {
		t.Errorf("perm=%q", p)
	}
}

func TestRunsActiveUniquenessAndTransitions(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	jid := newJob(t, db, "j1")
	r := Run{ID: "01A", JobID: &jid, JobName: "j1", Engine: "rsync", Trigger: TriggerManual, State: StateQueued,
		SpecJSON: "{}", QueuedAt: time.Now()}
	if err := db.InsertRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	r2 := r
	r2.ID = "01B"
	if err := db.InsertRun(ctx, r2); !errors.Is(err, ErrConflict) {
		t.Fatalf("second active run allowed: %v", err)
	}
	r3 := r
	fin := time.Now()
	r3.ID, r3.State, r3.FinishedAt = "01C", StateSkipped, &fin
	if err := db.InsertRun(ctx, r3); err != nil {
		t.Fatalf("skipped run: %v", err)
	}
	now := time.Now()
	if err := db.TransitionRun(ctx, "01A", StateQueued, StateRunning, RunPatch{StartedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if err := db.TransitionRun(ctx, "01A", StateQueued, StateRunning, RunPatch{}); !errors.Is(err, ErrStateConflict) {
		t.Errorf("stale transition: %v", err)
	}
	code := 0
	if err := db.TransitionRun(ctx, "01A", StateRunning, StateSucceeded, RunPatch{FinishedAt: &now, ExitCode: &code,
		Progress: &engine.Progress{BytesDone: 10, BytesTotal: 10, FilesDone: 1, FilesTotal: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertRun(ctx, r2); err != nil {
		t.Fatalf("new run after terminal: %v", err)
	}
	got, err := db.GetRun(ctx, "01A")
	if err != nil || got.State != StateSucceeded || got.BytesDone != 10 || *got.ExitCode != 0 || got.StartedAt == nil {
		t.Errorf("run: %+v %v", got, err)
	}
	list, _ := db.ListRuns(ctx, RunFilter{JobID: &jid, Limit: 2})
	if len(list) != 2 || list[0].ID != "01C" {
		t.Errorf("list: %d %v", len(list), list)
	}
	list, _ = db.ListRuns(ctx, RunFilter{States: ActiveStates})
	if len(list) != 1 || list[0].ID != "01B" {
		t.Errorf("active list: %v", list)
	}
	list, _ = db.ListRuns(ctx, RunFilter{JobIDs: []int64{}})
	if len(list) != 0 {
		t.Errorf("empty authz filter returned runs")
	}
	last, _ := db.LastRunsByJob(ctx)
	if last[jid].ID != "01C" {
		t.Errorf("last=%v", last[jid].ID)
	}
	if err := db.TransitionRun(ctx, "01B", StateQueued, StateCancelled, RunPatch{FinishedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteJob(ctx, jid); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetRun(ctx, "01A")
	if got.JobID != nil || got.JobName != "j1" {
		t.Errorf("run after job delete: %+v", got)
	}
	paths, err := db.PurgeRuns(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	_ = paths
	if l, _ := db.ListRuns(ctx, RunFilter{}); len(l) != 0 {
		t.Errorf("purge left %d runs", len(l))
	}
}

func TestSettingsAndAudit(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	if db.GetIntSetting(ctx, "max_concurrent", 2) != 2 {
		t.Error("default")
	}
	db.SetSetting(ctx, "max_concurrent", "4")
	db.SetSetting(ctx, "max_concurrent", "5")
	if db.GetIntSetting(ctx, "max_concurrent", 2) != 5 {
		t.Error("set")
	}
	db.AddAudit(ctx, nil, "job.create", "job:1", map[string]any{"name": "x"})
	l, _ := db.ListAudit(ctx, 0, 10)
	if len(l) != 1 || l[0].Details["name"] != "x" {
		t.Errorf("audit: %+v", l)
	}
}
