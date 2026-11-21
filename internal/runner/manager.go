// Package runner orchestre les exécutions : file d'attente, limite de
// concurrence, unicité par job, machine à états persistée, logs, reprise.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"rcsui/internal/engine"
	"rcsui/internal/events"
	"rcsui/internal/store"
)

// ErrJobActive : un run est déjà actif pour ce job.
var ErrJobActive = errors.New("a run is already active for this job")

// ErrShuttingDown : le serveur s'arrête.
var ErrShuttingDown = errors.New("server is shutting down")

// ErrNotActive : le run n'est pas actif.
var ErrNotActive = errors.New("run is not active")

// Deps sont les dépendances du Manager.
type Deps struct {
	DB      *store.DB
	Engines map[engine.Kind]engine.Engine
	Broker  *events.Broker
	LogDir  string
	Log     *slog.Logger
	// PersistInterval : fréquence d'écriture de la progression en base.
	PersistInterval time.Duration
	// CancelTimeout : délai max d'attente après une annulation avant de déclarer l'échec.
	CancelTimeout time.Duration
	// OnFinished est appelé (de façon synchrone, doit être rapide) à la fin de
	// chaque run : notifications, métriques.
	OnFinished []func(Finished)
	// RetryUnit : unité de job.RetryDelaySec (1 s par défaut ; réduite dans les tests).
	RetryUnit time.Duration
}

// Finished décrit un run terminé, pour les hooks.
type Finished struct {
	Run       store.Run  // état final (compteurs, erreur, horodatages)
	Retryable bool       // l'échec est jugé transitoire par le moteur
	RetryAt   *time.Time // relance automatique programmée, si applicable
}

// SpecSnapshot est le contenu de runs.spec_json : la définition au lancement.
type SpecSnapshot struct {
	JobID       int64           `json:"jobId"`
	Name        string          `json:"name"`
	Engine      engine.Kind     `json:"engine"`
	Mode        engine.Mode     `json:"mode"`
	Source      engine.Endpoint `json:"source"`
	Destination engine.Endpoint `json:"destination"`
	Options     json.RawMessage `json:"options"`
}

// StateEvent est le contenu d'un événement run.state.
type StateEvent struct {
	RunID    string           `json:"runId"`
	JobID    int64            `json:"jobId"`
	JobName  string           `json:"jobName"`
	From     store.RunState   `json:"from,omitempty"`
	To       store.RunState   `json:"to"`
	DryRun   bool             `json:"dryRun"`
	At       time.Time        `json:"at"`
	ExitCode *int             `json:"exitCode,omitempty"`
	Error    string           `json:"error,omitempty"`
	Progress *engine.Progress `json:"progress,omitempty"`
}

// ProgressEvent est le contenu d'un événement run.progress.
type ProgressEvent struct {
	RunID string         `json:"runId"`
	JobID int64          `json:"jobId"`
	State store.RunState `json:"state"`
	engine.Progress
	Percent float64 `json:"percent"`
}

type activeRun struct {
	run       store.Run
	spec      engine.Spec
	jobID     int64
	attempt   int
	state     store.RunState
	exec      engine.Execution
	sink      *runSink
	cancelReq bool
	shutdown  bool
	progress  engine.Progress
	lastGlob  time.Time
	lastSave  time.Time
	done      chan struct{}
}

// Manager gère les exécutions.
type Manager struct {
	d Deps

	mu      sync.Mutex
	max     int
	running int
	queue   []*activeRun
	active  map[string]*activeRun // par runID
	byJob   map[int64]*activeRun
	closing bool
	wg      sync.WaitGroup
	retries map[int64]*pendingRetry // relances automatiques programmées, par job
}

type pendingRetry struct {
	timer *time.Timer
	at    time.Time
}

// New crée un Manager.
func New(d Deps, maxConcurrent int) *Manager {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	if d.PersistInterval <= 0 {
		d.PersistInterval = 5 * time.Second
	}
	if d.CancelTimeout <= 0 {
		d.CancelTimeout = 30 * time.Second
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Manager{d: d, max: maxConcurrent, active: map[string]*activeRun{}, byJob: map[int64]*activeRun{},
		retries: map[int64]*pendingRetry{}}
}

// SetMaxConcurrent change la limite à chaud.
func (m *Manager) SetMaxConcurrent(n int) {
	if n < 1 {
		n = 1
	}
	m.mu.Lock()
	m.max = n
	m.mu.Unlock()
	m.dispatch()
}

// MaxConcurrent retourne la limite courante.
func (m *Manager) MaxConcurrent() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.max
}

// BuildSpec construit et valide la spec d'un job (utilisé aussi par l'API pour /jobs/validate).
func (m *Manager) BuildSpec(ctx context.Context, snap SpecSnapshot, runID string, dryRun bool) (engine.Spec, engine.Engine, error) {
	eng, ok := m.d.Engines[snap.Engine]
	if !ok {
		return engine.Spec{}, nil, &engine.ValidationError{Fields: map[string]string{"engine": "unknown or unavailable engine"}}
	}
	opts, err := eng.DecodeOptions(snap.Options)
	if err != nil {
		return engine.Spec{}, nil, err
	}
	s := engine.Spec{RunID: runID, Kind: snap.Engine, Mode: snap.Mode, Source: snap.Source,
		Destination: snap.Destination, DryRun: dryRun, Options: opts}
	if err := eng.Validate(ctx, s); err != nil {
		return engine.Spec{}, nil, err
	}
	return s, eng, nil
}

// SnapshotOf convertit un job en snapshot.
func SnapshotOf(j store.Job) SpecSnapshot {
	return SpecSnapshot{JobID: j.ID, Name: j.Name, Engine: j.Engine, Mode: j.Mode, Source: j.Source,
		Destination: j.Destination, Options: j.Options}
}

// Submit met en file un run pour un job.
func (m *Manager) Submit(ctx context.Context, jobID int64, trig store.Trigger, by *int64, dryRun bool) (string, error) {
	j, err := m.d.DB.GetJob(ctx, jobID)
	if err != nil {
		return "", err
	}
	return m.submitSnapshot(ctx, SnapshotOf(j), trig, by, dryRun, 1)
}

// Retry relance un run terminé avec la même spec.
func (m *Manager) Retry(ctx context.Context, runID string, by *int64) (string, error) {
	r, err := m.d.DB.GetRun(ctx, runID)
	if err != nil {
		return "", err
	}
	if !r.State.Terminal() {
		return "", ErrJobActive
	}
	var snap SpecSnapshot
	if err := json.Unmarshal([]byte(r.SpecJSON), &snap); err != nil {
		return "", fmt.Errorf("corrupt run spec: %w", err)
	}
	if r.JobID == nil {
		return "", fmt.Errorf("job was deleted: %w", store.ErrNotFound)
	}
	return m.submitSnapshot(ctx, snap, store.TriggerRetry, by, r.DryRun, 1)
}

func (m *Manager) submitSnapshot(ctx context.Context, snap SpecSnapshot, trig store.Trigger, by *int64, dryRun bool, attempt int) (string, error) {
	now := time.Now()
	id := NewID(now)
	spec, _, err := m.BuildSpec(ctx, snap, id, dryRun)
	if err != nil {
		return "", err
	}
	raw, _ := json.Marshal(snap)
	jid := snap.JobID
	logRel, _ := LogPaths(id, now)
	r := store.Run{ID: id, JobID: &jid, JobName: snap.Name, Engine: string(snap.Engine), Trigger: trig,
		TriggeredBy: by, DryRun: dryRun, Attempt: attempt, State: store.StateQueued, SpecJSON: string(raw), QueuedAt: now, LogPath: logRel}

	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return "", ErrShuttingDown
	}
	if _, busy := m.byJob[jid]; busy {
		m.mu.Unlock()
		return "", ErrJobActive
	}
	if err := m.d.DB.InsertRun(ctx, r); err != nil {
		m.mu.Unlock()
		if errors.Is(err, store.ErrConflict) {
			return "", ErrJobActive
		}
		return "", err
	}
	ar := &activeRun{run: r, spec: spec, jobID: jid, attempt: attempt, state: store.StateQueued, done: make(chan struct{})}
	m.active[id] = ar
	m.byJob[jid] = ar
	m.queue = append(m.queue, ar)
	// Un nouveau run (manuel, planifié ou la relance elle-même) remplace toute relance en attente.
	m.clearRetryLocked(jid)
	m.mu.Unlock()

	m.publishState(ar, "", store.StateQueued, nil, "")
	m.dispatch()
	return id, nil
}

// RecordSkipped enregistre un run "skipped" (planification sur un job déjà actif).
func (m *Manager) RecordSkipped(ctx context.Context, j store.Job, reason string) error {
	now := time.Now()
	raw, _ := json.Marshal(SnapshotOf(j))
	jid := j.ID
	r := store.Run{ID: NewID(now), JobID: &jid, JobName: j.Name, Engine: string(j.Engine), Trigger: store.TriggerSchedule,
		State: store.StateSkipped, Attempt: 1, SpecJSON: string(raw), QueuedAt: now, FinishedAt: &now, ErrorMessage: reason}
	if err := m.d.DB.InsertRun(ctx, r); err != nil {
		return err
	}
	m.runHooks(Finished{Run: r})
	m.d.Broker.PublishRun(r.ID, jid, events.TypeRunState,
		StateEvent{RunID: r.ID, JobID: jid, JobName: j.Name, To: store.StateSkipped, At: now, Error: reason}, true)
	m.d.Broker.EndRun(r.ID)
	return nil
}

// IsJobActive indique si un run est actif pour ce job.
func (m *Manager) IsJobActive(jobID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.byJob[jobID]
	return ok
}

// Live est l'état en mémoire d'un run actif.
type Live struct {
	State    store.RunState  `json:"state"`
	Progress engine.Progress `json:"progress"`
}

// Snapshot retourne l'état live d'un run actif.
func (m *Manager) Snapshot(runID string) (Live, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ar, ok := m.active[runID]
	if !ok {
		return Live{}, false
	}
	return Live{State: ar.state, Progress: ar.progress}, true
}

// ActiveRunIDs retourne les ids des runs actifs.
func (m *Manager) ActiveRunIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.active))
	for id := range m.active {
		out = append(out, id)
	}
	return out
}

func (m *Manager) dispatch() {
	m.mu.Lock()
	var start []*activeRun
	for !m.closing && m.running < m.max && len(m.queue) > 0 {
		ar := m.queue[0]
		m.queue = m.queue[1:]
		m.running++
		start = append(start, ar)
	}
	m.mu.Unlock()
	for _, ar := range start {
		m.wg.Add(1)
		go m.execute(ar)
	}
}

// Cancel annule un run actif (en file ou en cours).
func (m *Manager) Cancel(runID string) error {
	m.mu.Lock()
	ar, ok := m.active[runID]
	if !ok {
		m.mu.Unlock()
		return ErrNotActive
	}
	// En file : retrait direct.
	for i, q := range m.queue {
		if q == ar {
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			m.mu.Unlock()
			m.finishQueued(ar, store.StateCancelled, "cancelled before start")
			return nil
		}
	}
	if ar.cancelReq {
		m.mu.Unlock()
		return nil
	}
	ar.cancelReq = true
	x := ar.exec
	st := ar.state
	m.mu.Unlock()
	if x != nil && st == store.StateRunning {
		if err := m.transition(ar, store.StateRunning, store.StateCancelling, store.RunPatch{}); err != nil {
			m.d.Log.Warn("cancel transition", "run", runID, "err", err)
		}
		x.Cancel("requested by user")
	}
	// Si le run est en "starting", execute() verra cancelReq après Start.
	return nil
}

func (m *Manager) finishQueued(ar *activeRun, to store.RunState, msg string) {
	now := time.Now()
	ctx := context.Background()
	if err := m.d.DB.TransitionRun(ctx, ar.run.ID, store.StateQueued, to, store.RunPatch{FinishedAt: &now, ErrorMessage: &msg}); err != nil {
		m.d.Log.Error("finish queued run", "run", ar.run.ID, "err", err)
	}
	m.mu.Lock()
	ar.state = to
	m.release(ar, false)
	m.mu.Unlock()
	m.publishState(ar, store.StateQueued, to, nil, msg)
	m.d.Broker.EndRun(ar.run.ID)
	m.runHooks(Finished{Run: ar.finalRun(to, now, msg, nil, engine.Progress{})})
}

// release retire le run des structures actives. Appelé sous m.mu.
func (m *Manager) release(ar *activeRun, wasRunning bool) {
	delete(m.active, ar.run.ID)
	if m.byJob[ar.jobID] == ar {
		delete(m.byJob, ar.jobID)
	}
	if wasRunning {
		m.running--
	}
	select {
	case <-ar.done:
	default:
		close(ar.done)
	}
}

func (m *Manager) transition(ar *activeRun, from, to store.RunState, p store.RunPatch) error {
	if err := m.d.DB.TransitionRun(context.Background(), ar.run.ID, from, to, p); err != nil {
		return err
	}
	m.mu.Lock()
	ar.state = to
	m.mu.Unlock()
	var code *int
	if p.ExitCode != nil {
		code = p.ExitCode
	}
	msg := ""
	if p.ErrorMessage != nil {
		msg = *p.ErrorMessage
	}
	var prog *engine.Progress
	if to.Terminal() {
		prog = p.Progress
	}
	m.publishStateFull(ar, from, to, code, msg, prog)
	return nil
}

func (m *Manager) publishState(ar *activeRun, from, to store.RunState, code *int, msg string) {
	m.publishStateFull(ar, from, to, code, msg, nil)
}

func (m *Manager) publishStateFull(ar *activeRun, from, to store.RunState, code *int, msg string, prog *engine.Progress) {
	m.d.Broker.PublishRun(ar.run.ID, ar.jobID, events.TypeRunState, StateEvent{
		RunID: ar.run.ID, JobID: ar.jobID, JobName: ar.run.JobName, From: from, To: to, DryRun: ar.run.DryRun,
		At: time.Now(), ExitCode: code, Error: msg, Progress: prog,
	}, true)
}

func (m *Manager) onProgress(ar *activeRun, p engine.Progress) {
	now := time.Now()
	m.mu.Lock()
	ar.progress = p
	st := ar.state
	global := now.Sub(ar.lastGlob) >= time.Second
	if global {
		ar.lastGlob = now
	}
	save := now.Sub(ar.lastSave) >= m.d.PersistInterval
	if save {
		ar.lastSave = now
	}
	m.mu.Unlock()
	ev := ProgressEvent{RunID: ar.run.ID, JobID: ar.jobID, State: st, Progress: p, Percent: p.Percent()}
	m.d.Broker.PublishRun(ar.run.ID, ar.jobID, events.TypeRunProgress, ev, global)
	if save {
		pp := p
		if err := m.d.DB.UpdateRun(context.Background(), ar.run.ID, store.RunPatch{Progress: &pp}); err != nil {
			m.d.Log.Warn("persist progress", "run", ar.run.ID, "err", err)
		}
	}
}

func (m *Manager) execute(ar *activeRun) {
	defer m.wg.Done()
	defer m.dispatch()
	id := ar.run.ID
	log := m.d.Log.With("run", id, "job", ar.run.JobName)

	if err := m.transition(ar, store.StateQueued, store.StateStarting, store.RunPatch{}); err != nil {
		// Annulé entre-temps (Cancel/Shutdown a déjà traité le run).
		log.Info("run left queue before start", "err", err)
		m.mu.Lock()
		m.running--
		m.mu.Unlock()
		return
	}

	logRel, itemsRel := LogPaths(id, ar.run.QueuedAt)
	sink, err := newRunSink(m.d.LogDir, logRel, itemsRel, id, ar.jobID, m.d.Broker, func(p engine.Progress) { m.onProgress(ar, p) })
	if err != nil {
		m.fail(ar, store.StateStarting, fmt.Errorf("open log: %w", err), nil)
		return
	}
	ar.sink = sink

	ctx := context.Background()
	eng := m.d.Engines[ar.spec.Kind]
	// Revalidation au lancement (racines, remotes, known_hosts ont pu changer).
	if err := eng.Validate(ctx, ar.spec); err != nil {
		m.fail(ar, store.StateStarting, fmt.Errorf("validation at start: %w", err), sink)
		return
	}
	x, err := eng.Start(ctx, ar.spec, sink)
	if err != nil {
		m.fail(ar, store.StateStarting, err, sink)
		return
	}
	now := time.Now()
	ar.run.StartedAt = &now
	patch := store.RunPatch{StartedAt: &now}
	if pid := x.PID(); pid > 0 {
		patch.PID = &pid
	}
	if ext := x.ExternalID(); ext > 0 {
		patch.RcloneJobID = &ext
	}
	m.mu.Lock()
	ar.exec = x
	cancelNow := ar.cancelReq
	shutdown := ar.shutdown
	m.mu.Unlock()
	if err := m.transition(ar, store.StateStarting, store.StateRunning, patch); err != nil {
		log.Error("starting->running", "err", err)
	}
	if cancelNow {
		reason := "requested by user"
		if shutdown {
			reason = reasonShutdown
		}
		_ = m.transition(ar, store.StateRunning, store.StateCancelling, store.RunPatch{})
		x.Cancel(reason)
	}

	m.waitExecution(ar, x)
	res := x.Result()
	m.finish(ar, res, sink)
}

// waitExecution attend la fin, en bornant l'attente après une annulation.
func (m *Manager) waitExecution(ar *activeRun, x engine.Execution) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	var cancelSeen time.Time
	for {
		select {
		case <-x.Done():
			return
		case <-t.C:
			m.mu.Lock()
			req := ar.cancelReq
			m.mu.Unlock()
			if req && cancelSeen.IsZero() {
				cancelSeen = time.Now()
			}
			if req && time.Since(cancelSeen) > m.d.CancelTimeout {
				m.d.Log.Error("cancellation not confirmed in time", "run", ar.run.ID)
				return
			}
		}
	}
}

func (m *Manager) finish(ar *activeRun, res engine.Result, sink *runSink) {
	m.mu.Lock()
	from := ar.state
	shutdown := ar.shutdown
	m.mu.Unlock()

	to := outcomeState(res.Outcome)
	msg := ""
	if res.Err != nil {
		msg = res.Err.Error()
	}
	if res.Outcome == "" {
		// waitExecution a abandonné : annulation non confirmée.
		to, msg = store.StateFailed, "cancellation not confirmed"
	}
	if shutdown && (to == store.StateCancelled || to == store.StateFailed) {
		to, msg = store.StateInterrupted, reasonShutdown
	}
	sink.OnLog(engine.LogLine{Time: time.Now(), Stream: "system", Level: levelFor(to), Msg: "run finished: " + string(to) + suffix(msg)})
	size := sink.Close()
	now := time.Now()
	final := res.Final
	patch := store.RunPatch{FinishedAt: &now, ExitCode: res.ExitCode, Progress: &final, LogSize: &size}
	if msg != "" {
		patch.ErrorMessage = &msg
	}
	if err := m.transition(ar, from, to, patch); err != nil {
		m.d.Log.Error("final transition", "run", ar.run.ID, "from", from, "to", to, "err", err)
	}
	m.mu.Lock()
	ar.progress = final
	m.release(ar, true)
	m.mu.Unlock()
	m.d.Broker.EndRun(ar.run.ID)

	f := Finished{Run: ar.finalRun(to, now, msg, res.ExitCode, final), Retryable: res.Retryable}
	if to == store.StateFailed && res.Retryable && !shutdown {
		f.RetryAt = m.scheduleRetry(ar)
	}
	m.runHooks(f)
}

func (m *Manager) fail(ar *activeRun, from store.RunState, err error, sink *runSink) {
	msg := err.Error()
	m.d.Log.Warn("run failed to start", "run", ar.run.ID, "err", msg)
	var size int64
	if sink != nil {
		sink.OnLog(engine.LogLine{Time: time.Now(), Stream: "system", Level: engine.LevelError, Msg: msg})
		size = sink.Close()
	}
	now := time.Now()
	to := store.StateFailed
	m.mu.Lock()
	if ar.shutdown {
		to = store.StateInterrupted
	}
	m.mu.Unlock()
	if terr := m.transition(ar, from, to, store.RunPatch{FinishedAt: &now, ErrorMessage: &msg, LogSize: &size}); terr != nil {
		m.d.Log.Error("fail transition", "run", ar.run.ID, "err", terr)
	}
	m.mu.Lock()
	m.release(ar, true)
	m.mu.Unlock()
	m.d.Broker.EndRun(ar.run.ID)
	// Un échec de validation au lancement n'est pas transitoire : pas de relance automatique.
	m.runHooks(Finished{Run: ar.finalRun(to, now, msg, nil, engine.Progress{})})
}

func outcomeState(o engine.Outcome) store.RunState {
	switch o {
	case engine.OutcomeSucceeded:
		return store.StateSucceeded
	case engine.OutcomePartial:
		return store.StatePartial
	case engine.OutcomeCancelled:
		return store.StateCancelled
	default:
		return store.StateFailed
	}
}

func levelFor(s store.RunState) engine.LogLevel {
	switch s {
	case store.StateSucceeded:
		return engine.LevelInfo
	case store.StatePartial, store.StateCancelled, store.StateInterrupted:
		return engine.LevelWarn
	default:
		return engine.LevelError
	}
}

func suffix(msg string) string {
	if msg == "" {
		return ""
	}
	return " (" + msg + ")"
}

// Shutdown annule tous les runs et attend leur fin (borne : ctx).
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closing = true
	for jid := range m.retries {
		m.clearRetryLocked(jid)
	}
	queued := m.queue
	m.queue = nil
	var running []*activeRun
	for _, ar := range m.active {
		ar.shutdown = true
		ar.cancelReq = true
		if ar.exec != nil {
			running = append(running, ar)
		}
	}
	m.mu.Unlock()
	for _, ar := range queued {
		m.finishQueued(ar, store.StateInterrupted, reasonShutdown)
	}
	for _, ar := range running {
		_ = m.transition(ar, store.StateRunning, store.StateCancelling, store.RunPatch{})
		ar.exec.Cancel(reasonShutdown)
	}
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		// Les runs restants seront marqués "interrupted" au prochain démarrage.
		return ctx.Err()
	}
}

// reasonShutdown est le motif utilisé pour les runs arrêtés par le shutdown.
const reasonShutdown = "server shutdown"
