package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"rcsui/internal/store"
)

type fakeRunner struct{}

func (fakeRunner) StateCounts() map[store.RunState]int {
	return map[store.RunState]int{store.StateQueued: 1, store.StateRunning: 2}
}
func (fakeRunner) MaxConcurrent() int { return 3 }

type fakeDB struct{}

var t0 = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

func (fakeDB) ListJobs(context.Context) ([]store.Job, error) {
	return []store.Job{
		{ID: 1, Name: `photos "nas"`, Engine: "rsync", ScheduleOn: true, ScheduleCron: "@daily"},
		{ID: 2, Name: "cloud", Engine: "rclone"},
	}, nil
}
func (fakeDB) LastRunsByJob(context.Context) (map[int64]store.Run, error) {
	start, end := t0, t0.Add(90*time.Second)
	return map[int64]store.Run{
		1: {State: store.StateFailed, StartedAt: &start, FinishedAt: &end},
		2: {State: store.StateRunning},
	}, nil
}
func (fakeDB) LastSuccessByJob(context.Context) (map[int64]time.Time, error) {
	return map[int64]time.Time{1: t0.Add(-24 * time.Hour)}, nil
}

func TestHandler(t *testing.T) {
	m := &Metrics{Runner: fakeRunner{}, DB: fakeDB{}, Build: BuildInfo{Version: "1.0", RsyncVersion: "3.5.2"}}
	m.RunFinished(store.Run{Engine: "rsync", State: store.StateSucceeded, BytesDone: 100, FilesDone: 2})
	m.RunFinished(store.Run{Engine: "rsync", State: store.StateSucceeded, BytesDone: 50, FilesDone: 1})
	m.RunFinished(store.Run{Engine: "rclone", State: store.StateFailed, DryRun: true, BytesDone: 999})
	h := m.Handler("s3cret")

	for _, auth := range []string{"", "Bearer nope", "s3cret"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("auth %q: status %d", auth, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`rcsui_build_info{version="1.0",rclone_version="",rsync_version="3.5.2"} 1`,
		`rcsui_runs_active{state="running"} 2`,
		`rcsui_max_concurrent_runs 3`,
		`rcsui_runs_finished_total{engine="rsync",state="succeeded"} 2`,
		`rcsui_runs_finished_total{engine="rclone",state="failed"} 1`,
		`rcsui_transferred_bytes_total{engine="rsync"} 150`,
		`rcsui_jobs 2`,
		`rcsui_jobs_scheduled 1`,
		`rcsui_job_last_run_success{job="photos \"nas\"",job_id="1",engine="rsync"} 0`,
		`rcsui_job_last_run_duration_seconds{job="photos \"nas\"",job_id="1",engine="rsync"} 90`,
		`rcsui_job_last_success_timestamp_seconds{job="photos \"nas\"",job_id="1",engine="rsync"} ` +
			strconv.FormatInt(t0.Add(-24*time.Hour).Unix(), 10),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s\n---\n%s", want, body)
		}
	}
	// Les runs dry-run ne comptent pas dans les octets transférés.
	if strings.Contains(body, `rcsui_transferred_bytes_total{engine="rclone"} 999`) {
		t.Error("dry-run bytes counted")
	}
	// Job dont le dernier run est actif : pas de série last_run.
	if strings.Contains(body, `rcsui_job_last_run_success{job="cloud"`) {
		t.Error("active run exported as last finished run")
	}
	assertGrouped(t, body)
}

// assertGrouped vérifie que les échantillons de chaque famille sont contigus.
func assertGrouped(t *testing.T, body string) {
	t.Helper()
	seen := map[string]bool{}
	cur := ""
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		name := line[:strings.IndexAny(line, "{ ")]
		if name != cur {
			if seen[name] {
				t.Errorf("family %s is not contiguous", name)
			}
			seen[name], cur = true, name
		}
	}
}
