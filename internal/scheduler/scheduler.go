// Package scheduler déclenche les jobs planifiés (robfig/cron/v3, encapsulé :
// remplaçable sans toucher au reste, cf. PLAN.md §12 risque 8).
package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"rcsui/internal/runner"
	"rcsui/internal/store"
)

// Runner est le sous-ensemble du runner utilisé par le scheduler.
type Runner interface {
	Submit(ctx context.Context, jobID int64, trig store.Trigger, by *int64, dryRun bool) (string, error)
	RecordSkipped(ctx context.Context, j store.Job, reason string) error
	IsJobActive(jobID int64) bool
}

// JobSource fournit les jobs.
type JobSource interface {
	ListJobs(ctx context.Context) ([]store.Job, error)
	GetJob(ctx context.Context, id int64) (store.Job, error)
	LastScheduledRunAt(ctx context.Context, jobID int64) (time.Time, error)
}

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// Parse valide une expression cron standard (5 champs) ou un descripteur (@daily, @every 1h...).
func Parse(expr string) (cron.Schedule, error) {
	if expr == "" {
		return nil, errors.New("empty cron expression")
	}
	if len(expr) > 128 {
		return nil, errors.New("cron expression too long")
	}
	s, err := parser.Parse(expr)
	if err != nil {
		return nil, err
	}
	// Garde-fou : pas plus d'une exécution par minute.
	if ev, ok := s.(cron.ConstantDelaySchedule); ok && ev.Delay < time.Minute {
		return nil, errors.New("schedules more frequent than once a minute are not allowed")
	}
	return s, nil
}

// Preview retourne les n prochaines occurrences après from.
func Preview(expr string, n int, from time.Time) ([]time.Time, error) {
	s, err := Parse(expr)
	if err != nil {
		return nil, err
	}
	out := make([]time.Time, 0, n)
	t := from
	for i := 0; i < n; i++ {
		t = s.Next(t)
		if t.IsZero() {
			break
		}
		out = append(out, t)
	}
	return out, nil
}

// Scheduler synchronise les définitions planifiées avec cron.
type Scheduler struct {
	c      *cron.Cron
	runner Runner
	jobs   JobSource
	log    *slog.Logger
	now    func() time.Time

	mu      sync.Mutex
	entries map[int64]entry
}

type entry struct {
	id   cron.EntryID
	expr string
}

type slogAdapter struct{ l *slog.Logger }

func (a slogAdapter) Info(msg string, kv ...any) { a.l.Debug(msg, kv...) }
func (a slogAdapter) Error(err error, msg string, kv ...any) {
	a.l.Error(msg, append(kv, "err", err)...)
}

// New crée un scheduler dans le fuseau loc.
func New(r Runner, jobs JobSource, loc *time.Location, log *slog.Logger) *Scheduler {
	if loc == nil {
		loc = time.Local
	}
	if log == nil {
		log = slog.Default()
	}
	lg := slogAdapter{log}
	return &Scheduler{
		// cron.Recover explicite : la doc et le README divergent sur le comportement par défaut.
		c:       cron.New(cron.WithLocation(loc), cron.WithParser(parser), cron.WithLogger(lg), cron.WithChain(cron.Recover(lg))),
		runner:  r,
		jobs:    jobs,
		log:     log,
		now:     time.Now,
		entries: map[int64]entry{},
	}
}

// Start charge les jobs planifiés et démarre le scheduler.
func (s *Scheduler) Start(ctx context.Context) error {
	jobs, err := s.jobs.ListJobs(ctx)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if err := s.Sync(j); err != nil {
			s.log.Warn("invalid schedule ignored", "job", j.Name, "err", err)
		}
	}
	s.c.Start()
	for _, j := range jobs {
		if j.CatchUp && j.ScheduleOn && j.ScheduleCron != "" {
			s.catchUp(ctx, j)
		}
	}
	return nil
}

// catchUp exécute une fois un job dont au moins une occurrence planifiée est
// tombée pendant l'arrêt du serveur (option par job, PLAN.md §4.5 point 4).
// Référence : dernier run planifié, ou la dernière modification du job si elle
// est plus récente (un cron modifié ne doit pas « rattraper » l'ancien).
func (s *Scheduler) catchUp(ctx context.Context, j store.Job) {
	sched, err := Parse(j.ScheduleCron)
	if err != nil {
		return
	}
	ref, err := s.jobs.LastScheduledRunAt(ctx, j.ID)
	if err != nil {
		s.log.Warn("catch-up: last scheduled run", "job", j.Name, "err", err)
		return
	}
	if j.UpdatedAt.After(ref) {
		ref = j.UpdatedAt
	}
	missed := sched.Next(ref)
	if missed.IsZero() || !missed.Before(s.now()) {
		return
	}
	if s.runner.IsJobActive(j.ID) {
		// Déjà relancé (retry_interrupted) : rien à rattraper de plus.
		return
	}
	s.log.Info("catching up missed scheduled run", "job", j.Name, "missed", missed.Format(time.RFC3339))
	if _, err := s.runner.Submit(ctx, j.ID, store.TriggerSchedule, nil, false); err != nil {
		s.log.Warn("catch-up submit failed", "job", j.Name, "err", err)
	}
}

// Stop arrête le scheduler.
func (s *Scheduler) Stop() { s.c.Stop() }

// Sync ajoute, met à jour ou retire la planification d'un job.
func (s *Scheduler) Sync(j store.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, has := s.entries[j.ID]
	want := j.ScheduleOn && j.ScheduleCron != ""
	if has && (!want || cur.expr != j.ScheduleCron) {
		s.c.Remove(cur.id)
		delete(s.entries, j.ID)
		has = false
	}
	if !want || has {
		return nil
	}
	sched, err := Parse(j.ScheduleCron)
	if err != nil {
		return err
	}
	jobID := j.ID
	id := s.c.Schedule(sched, cron.FuncJob(func() { s.fire(jobID) }))
	s.entries[j.ID] = entry{id: id, expr: j.ScheduleCron}
	return nil
}

// Remove retire un job du scheduler.
func (s *Scheduler) Remove(jobID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[jobID]; ok {
		s.c.Remove(e.id)
		delete(s.entries, jobID)
	}
}

// Next retourne la prochaine exécution planifiée d'un job.
func (s *Scheduler) Next(jobID int64) *time.Time {
	s.mu.Lock()
	e, ok := s.entries[jobID]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	n := s.c.Entry(e.id).Next
	if n.IsZero() {
		// Scheduler pas encore démarré : calcul direct.
		if sc, err := Parse(e.expr); err == nil {
			n = sc.Next(time.Now())
		}
	}
	if n.IsZero() {
		return nil
	}
	return &n
}

func (s *Scheduler) fire(jobID int64) {
	ctx := context.Background()
	j, err := s.jobs.GetJob(ctx, jobID)
	if err != nil {
		s.log.Warn("scheduled job vanished", "job", jobID, "err", err)
		s.Remove(jobID)
		return
	}
	if s.runner.IsJobActive(jobID) {
		if err := s.runner.RecordSkipped(ctx, j, "previous run still active"); err != nil {
			s.log.Error("record skipped run", "job", j.Name, "err", err)
		}
		return
	}
	if _, err := s.runner.Submit(ctx, jobID, store.TriggerSchedule, nil, false); err != nil {
		reason := "could not start: " + err.Error()
		if errors.Is(err, runner.ErrDestinationBusy) {
			reason = err.Error() // attendu : option « destination exclusive » du job
			s.log.Info("scheduled run skipped", "job", j.Name, "reason", reason)
		} else {
			s.log.Error("scheduled submit failed", "job", j.Name, "err", err)
		}
		if err2 := s.runner.RecordSkipped(ctx, j, reason); err2 != nil {
			s.log.Error("record skipped run", "job", j.Name, "err", err2)
		}
	}
}
