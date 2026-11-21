package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"rcsui/internal/store"
)

type fakeRunner struct {
	mu      sync.Mutex
	active  bool
	submits []int64
	skips   []string
	err     error
}

func (f *fakeRunner) Submit(_ context.Context, id int64, trig store.Trigger, _ *int64, _ bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if trig != store.TriggerSchedule {
		panic("bad trigger")
	}
	f.submits = append(f.submits, id)
	return "R", f.err
}
func (f *fakeRunner) RecordSkipped(_ context.Context, _ store.Job, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.skips = append(f.skips, reason)
	return nil
}
func (f *fakeRunner) IsJobActive(int64) bool { f.mu.Lock(); defer f.mu.Unlock(); return f.active }

type fakeJobs map[int64]store.Job

func (f fakeJobs) ListJobs(context.Context) ([]store.Job, error) {
	var out []store.Job
	for _, j := range f {
		out = append(out, j)
	}
	return out, nil
}
func (f fakeJobs) GetJob(_ context.Context, id int64) (store.Job, error) {
	j, ok := f[id]
	if !ok {
		return j, store.ErrNotFound
	}
	return j, nil
}

func TestParseAndPreview(t *testing.T) {
	for _, bad := range []string{"", "* * *", "@every 10s", "61 * * * *", "0 0 * * * *"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	from := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	got, err := Preview("0 3 * * *", 3, from)
	if err != nil || len(got) != 3 || !got[0].Equal(time.Date(2026, 11, 22, 3, 0, 0, 0, time.UTC)) {
		t.Errorf("preview %v %v", got, err)
	}
	if _, err := Preview("@daily", 1, from); err != nil {
		t.Error(err)
	}
}

func TestFireSubmitSkip(t *testing.T) {
	r := &fakeRunner{}
	jobs := fakeJobs{1: {ID: 1, Name: "j", ScheduleCron: "* * * * *", ScheduleOn: true}}
	s := New(r, jobs, time.UTC, nil)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	if s.Next(1) == nil {
		t.Fatal("job not scheduled")
	}
	s.fire(1)
	r.mu.Lock()
	r.active = true
	r.mu.Unlock()
	s.fire(1)
	r.mu.Lock()
	r.active, r.err = false, errors.New("boom")
	r.mu.Unlock()
	s.fire(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.submits) != 2 || len(r.skips) != 2 || r.skips[0] != "previous run still active" {
		t.Errorf("submits=%v skips=%v", r.submits, r.skips)
	}
	s.fire(99) // job supprimé : pas de panique
}

func TestSyncUpdatesAndRemoves(t *testing.T) {
	s := New(&fakeRunner{}, fakeJobs{}, time.UTC, nil)
	j := store.Job{ID: 5, ScheduleCron: "0 3 * * *", ScheduleOn: true}
	if err := s.Sync(j); err != nil {
		t.Fatal(err)
	}
	s.c.Start()
	defer s.Stop()
	j.ScheduleCron = "0 4 * * *"
	if err := s.Sync(j); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if n := s.Next(5); n == nil || n.Hour() != 4 {
		t.Errorf("update: %v", n)
	}
	j.ScheduleOn = false
	s.Sync(j)
	if s.Next(5) != nil {
		t.Error("disabled schedule still present")
	}
	j.ScheduleOn, j.ScheduleCron = true, "bad"
	if err := s.Sync(j); err == nil {
		t.Error("bad cron accepted")
	}
}

func (f fakeJobs) LastScheduledRunAt(context.Context, int64) (time.Time, error) {
	return time.Time{}, nil
}

type catchJobs struct {
	fakeJobs
	last map[int64]time.Time
}

func (c catchJobs) LastScheduledRunAt(_ context.Context, id int64) (time.Time, error) {
	return c.last[id], nil
}

func TestCatchUpMissedRuns(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	created := now.Add(-30 * 24 * time.Hour)
	daily := "0 3 * * *"
	jobs := catchJobs{
		fakeJobs: fakeJobs{
			// Dernier run planifié avant-hier : l'occurrence d'hier 03:00 a été manquée.
			1: {ID: 1, Name: "missed", ScheduleCron: daily, ScheduleOn: true, CatchUp: true, UpdatedAt: created},
			// A tourné ce matin à 03:00 : rien à rattraper.
			2: {ID: 2, Name: "uptodate", ScheduleCron: daily, ScheduleOn: true, CatchUp: true, UpdatedAt: created},
			// Manqué, mais l'option est désactivée.
			3: {ID: 3, Name: "nocatchup", ScheduleCron: daily, ScheduleOn: true, UpdatedAt: created},
			// Cron modifié il y a une heure : la référence est la modification, pas l'ancien run.
			4: {ID: 4, Name: "edited", ScheduleCron: daily, ScheduleOn: true, CatchUp: true, UpdatedAt: now.Add(-time.Hour)},
			// Planification désactivée.
			5: {ID: 5, Name: "off", ScheduleCron: daily, CatchUp: true, UpdatedAt: created},
		},
		last: map[int64]time.Time{
			1: now.Add(-57 * time.Hour),
			2: now.Add(-9 * time.Hour),
			3: now.Add(-57 * time.Hour),
			4: now.Add(-57 * time.Hour),
			5: now.Add(-57 * time.Hour),
		},
	}
	r := &fakeRunner{}
	s := New(r, jobs, time.UTC, nil)
	s.now = func() time.Time { return now }
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.submits) != 1 || r.submits[0] != 1 {
		t.Fatalf("catch-up submits = %v, want [1]", r.submits)
	}
}

func TestCatchUpSkipsActiveJob(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	jobs := catchJobs{
		fakeJobs: fakeJobs{1: {ID: 1, Name: "j", ScheduleCron: "@hourly", ScheduleOn: true, CatchUp: true, UpdatedAt: now.Add(-48 * time.Hour)}},
		last:     map[int64]time.Time{1: now.Add(-5 * time.Hour)},
	}
	r := &fakeRunner{active: true}
	s := New(r, jobs, time.UTC, nil)
	s.now = func() time.Time { return now }
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.submits) != 0 {
		t.Fatalf("active job must not be caught up: %v", r.submits)
	}
}
