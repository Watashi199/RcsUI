// Package metrics expose /metrics au format texte Prometheus (jalon M7).
// Pas de dépendance au client Prometheus : quelques compteurs en mémoire
// (alimentés par le hook de fin de run) et des jauges lues à chaque scrape
// (runner et base). L'accès exige un jeton bearer (RCSUI_METRICS_TOKEN) ;
// sans jeton configuré, l'endpoint n'est pas monté.
package metrics

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rcsui/internal/store"
)

// Runner est le sous-ensemble du runner lu au scrape.
type Runner interface {
	StateCounts() map[store.RunState]int
	MaxConcurrent() int
}

// DB est le sous-ensemble du store lu au scrape.
type DB interface {
	ListJobs(ctx context.Context) ([]store.Job, error)
	LastRunsByJob(ctx context.Context) (map[int64]store.Run, error)
	LastSuccessByJob(ctx context.Context) (map[int64]time.Time, error)
}

// BuildInfo alimente rcsui_build_info.
type BuildInfo struct {
	Version, RcloneVersion, RsyncVersion string
}

// Metrics collecte et sert les métriques.
type Metrics struct {
	Runner Runner
	DB     DB
	Build  BuildInfo
	Log    *slog.Logger

	mu       sync.Mutex
	finished map[[2]string]uint64 // (engine, state)
	bytes    map[string]uint64    // par moteur, runs réels seulement
	files    map[string]uint64
}

// RunFinished met à jour les compteurs (hook du runner).
func (m *Metrics) RunFinished(r store.Run) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.finished == nil {
		m.finished, m.bytes, m.files = map[[2]string]uint64{}, map[string]uint64{}, map[string]uint64{}
	}
	m.finished[[2]string{r.Engine, string(r.State)}]++
	if !r.DryRun {
		m.bytes[r.Engine] += uint64(max(r.BytesDone, 0))
		m.files[r.Engine] += uint64(max(r.FilesDone, 0))
	}
}

// Handler sert /metrics, protégé par le jeton bearer token.
func (m *Metrics) Handler(token string) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if token == "" || subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="rcsui metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if err := m.Write(r.Context(), w); err != nil && m.Log != nil {
			m.Log.Warn("metrics scrape", "err", err)
		}
	})
}

// Write écrit toutes les métriques. Au format texte, les échantillons d'une
// famille suivent immédiatement ses lignes HELP/TYPE.
func (m *Metrics) Write(ctx context.Context, w io.Writer) error {
	e := &enc{w: w}
	e.family("rcsui_build_info", "gauge", "Build information.")
	e.sample("rcsui_build_info", labels{"version", m.Build.Version, "rclone_version", m.Build.RcloneVersion,
		"rsync_version", m.Build.RsyncVersion}, 1)

	if m.Runner != nil {
		e.family("rcsui_runs_active", "gauge", "Runs currently queued or executing, by state.")
		counts := m.Runner.StateCounts()
		for _, st := range sortedKeys(counts) {
			e.sample("rcsui_runs_active", labels{"state", st}, float64(counts[store.RunState(st)]))
		}
		e.family("rcsui_max_concurrent_runs", "gauge", "Maximum number of concurrent runs.")
		e.sample("rcsui_max_concurrent_runs", nil, float64(m.Runner.MaxConcurrent()))
	}

	m.mu.Lock()
	e.family("rcsui_runs_finished_total", "counter", "Runs finished since the server started, by engine and final state.")
	keys := make([][2]string, 0, len(m.finished))
	for k := range m.finished {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	for _, k := range keys {
		e.sample("rcsui_runs_finished_total", labels{"engine", k[0], "state", k[1]}, float64(m.finished[k]))
	}
	e.family("rcsui_transferred_bytes_total", "counter", "Bytes transferred by real (non dry-run) runs since the server started.")
	for _, k := range sortedKeys(m.bytes) {
		e.sample("rcsui_transferred_bytes_total", labels{"engine", k}, float64(m.bytes[k]))
	}
	e.family("rcsui_transferred_files_total", "counter", "Files transferred by real (non dry-run) runs since the server started.")
	for _, k := range sortedKeys(m.files) {
		e.sample("rcsui_transferred_files_total", labels{"engine", k}, float64(m.files[k]))
	}
	m.mu.Unlock()

	if m.DB == nil {
		return e.err
	}
	jobs, err := m.DB.ListJobs(ctx)
	if err != nil {
		return err
	}
	last, err := m.DB.LastRunsByJob(ctx)
	if err != nil {
		return err
	}
	success, err := m.DB.LastSuccessByJob(ctx)
	if err != nil {
		return err
	}
	scheduled := 0
	for _, j := range jobs {
		if j.ScheduleOn && j.ScheduleCron != "" {
			scheduled++
		}
	}
	e.family("rcsui_jobs", "gauge", "Number of job definitions.")
	e.sample("rcsui_jobs", nil, float64(len(jobs)))
	e.family("rcsui_jobs_scheduled", "gauge", "Number of jobs with an enabled schedule.")
	e.sample("rcsui_jobs_scheduled", nil, float64(scheduled))

	e.family("rcsui_job_last_success_timestamp_seconds", "gauge", "End time of the last successful real run of each job.")
	for _, j := range jobs {
		if t, ok := success[j.ID]; ok {
			e.sample("rcsui_job_last_success_timestamp_seconds", jobLabels(j), unix(t))
		}
	}

	// Dernier run terminé de chaque job (les runs actifs sont ignorés).
	type fin struct {
		j store.Job
		r store.Run
	}
	var done []fin
	for _, j := range jobs {
		if r, ok := last[j.ID]; ok && r.State.Terminal() && r.FinishedAt != nil {
			done = append(done, fin{j, r})
		}
	}
	e.family("rcsui_job_last_run_timestamp_seconds", "gauge", "End time of the last finished run of each job.")
	for _, f := range done {
		e.sample("rcsui_job_last_run_timestamp_seconds", jobLabels(f.j), unix(*f.r.FinishedAt))
	}
	e.family("rcsui_job_last_run_success", "gauge", "1 if the last finished run of each job succeeded, 0 otherwise.")
	for _, f := range done {
		v := 0.0
		if f.r.State == store.StateSucceeded {
			v = 1
		}
		e.sample("rcsui_job_last_run_success", jobLabels(f.j), v)
	}
	e.family("rcsui_job_last_run_duration_seconds", "gauge", "Duration of the last finished run of each job.")
	for _, f := range done {
		if f.r.StartedAt != nil {
			e.sample("rcsui_job_last_run_duration_seconds", jobLabels(f.j), f.r.FinishedAt.Sub(*f.r.StartedAt).Seconds())
		}
	}
	return e.err
}

func jobLabels(j store.Job) labels {
	return labels{"job", j.Name, "job_id", strconv.FormatInt(j.ID, 10), "engine", string(j.Engine)}
}

func unix(t time.Time) float64 { return float64(t.UnixMilli()) / 1000 }

func sortedKeys[K ~string, V any](m map[K]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, string(k))
	}
	sort.Strings(out)
	return out
}

// labels est une liste plate nom, valeur, nom, valeur…
type labels []string

type enc struct {
	w   io.Writer
	err error
}

func (e *enc) family(name, typ, help string) {
	e.printf("# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

func (e *enc) sample(name string, l labels, v float64) {
	if len(l) == 0 {
		e.printf("%s %s\n", name, fmtFloat(v))
		return
	}
	var b strings.Builder
	for i := 0; i+1 < len(l); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(l[i])
		b.WriteString(`="`)
		b.WriteString(labelEscaper.Replace(l[i+1]))
		b.WriteByte('"')
	}
	e.printf("%s{%s} %s\n", name, b.String(), fmtFloat(v))
}

func (e *enc) printf(format string, a ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, a...)
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func fmtFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
