package rclone

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"rcsui/internal/engine"
	"rcsui/internal/paths"
)

// Options est la liste blanche des options rclone exposées à l'UI.
type Options struct {
	V                  int      `json:"v"`
	Transfers          *int     `json:"transfers,omitempty"`
	Checkers           *int     `json:"checkers,omitempty"`
	Checksum           bool     `json:"checksum,omitempty"`
	SizeOnly           bool     `json:"sizeOnly,omitempty"`
	IgnoreExisting     bool     `json:"ignoreExisting,omitempty"`
	Update             bool     `json:"update,omitempty"`
	TrackRenames       bool     `json:"trackRenames,omitempty"`
	CreateEmptySrcDirs bool     `json:"createEmptySrcDirs,omitempty"`
	DeleteEmptySrcDirs bool     `json:"deleteEmptySrcDirs,omitempty"`
	DeleteExcluded     bool     `json:"deleteExcluded,omitempty"`
	MaxDelete          *int64   `json:"maxDelete,omitempty"`
	Excludes           []string `json:"excludes,omitempty"`
	Includes           []string `json:"includes,omitempty"`
	MaxAge             string   `json:"maxAge,omitempty"`
	MinAge             string   `json:"minAge,omitempty"`
	MaxSize            string   `json:"maxSize,omitempty"`
	MinSize            string   `json:"minSize,omitempty"`
	// Bisync : options du mode bisync (ignoré, et refusé s'il est renseigné, dans les autres modes).
	Bisync *BisyncOptions `json:"bisync,omitempty"`
}

var (
	durationRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?(ms|s|m|h|d|w|M|y)$`)
	sizeOptRe  = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[BKMGTP]?$`)
)

// DecodeOptions décode strictement et valide.
func DecodeOptions(raw []byte) (*Options, error) {
	o := &Options{V: 1}
	if len(bytes.TrimSpace(raw)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(o); err != nil {
			return nil, &engine.ValidationError{Fields: map[string]string{"options": err.Error()}}
		}
	}
	ve := &engine.ValidationError{}
	if o.V != 1 {
		ve.Add("options.v", "unsupported options version")
	}
	if o.Transfers != nil && (*o.Transfers < 1 || *o.Transfers > 64) {
		ve.Add("options.transfers", "must be between 1 and 64")
	}
	if o.Checkers != nil && (*o.Checkers < 1 || *o.Checkers > 128) {
		ve.Add("options.checkers", "must be between 1 and 128")
	}
	if o.MaxDelete != nil && *o.MaxDelete < 0 {
		ve.Add("options.maxDelete", "must be >= 0")
	}
	for f, v := range map[string]string{"options.maxAge": o.MaxAge, "options.minAge": o.MinAge} {
		if v != "" && !durationRe.MatchString(v) {
			ve.Add(f, "invalid duration (e.g. 7d, 12h)")
		}
	}
	for f, v := range map[string]string{"options.maxSize": o.MaxSize, "options.minSize": o.MinSize} {
		if v != "" && !sizeOptRe.MatchString(v) {
			ve.Add(f, "invalid size (e.g. 100M)")
		}
	}
	if len(o.Excludes)+len(o.Includes) > 200 {
		ve.Add("options.excludes", "too many patterns")
	}
	for i, p := range append(append([]string{}, o.Includes...), o.Excludes...) {
		if p == "" || len(p) > 1024 || bytes.ContainsAny([]byte(p), "\x00\n\r") {
			ve.Add("options.patterns."+strconv.Itoa(i), "invalid pattern")
		}
	}
	if o.Bisync != nil {
		o.Bisync.validate(ve)
	}
	if err := ve.Err(); err != nil {
		return nil, err
	}
	return o, nil
}

// Engine est le moteur rclone (in-process).
type Engine struct {
	Client *Client
	Paths  *paths.Resolver
	// PollInterval : fréquence de lecture des stats (1 s par défaut).
	PollInterval time.Duration
	// StopTimeout : délai d'attente après job/stop (30 s par défaut).
	StopTimeout time.Duration
	// BisyncDir : répertoire de travail de bisync (listings de référence,
	// verrous, fichiers de filtres). Vide : mode bisync indisponible.
	BisyncDir string
}

var _ engine.Engine = (*Engine)(nil)

func (e *Engine) Kind() engine.Kind { return engine.KindRclone }

func (e *Engine) DecodeOptions(raw []byte) (any, error) { return DecodeOptions(raw) }

type built struct {
	method string
	in     map[string]any
	// files : fichiers à écrire avant l'appel (chemin absolu → contenu).
	files map[string][]byte
}

func (e *Engine) build(s engine.Spec) (built, error) {
	o, ok := s.Options.(*Options)
	if !ok || o == nil {
		return built{}, fmt.Errorf("rclone: options not decoded")
	}
	ve := &engine.ValidationError{}
	method := map[engine.Mode]string{engine.ModeCopy: "sync/copy", engine.ModeSync: "sync/sync", engine.ModeMove: "sync/move",
		engine.ModeBisync: "sync/bisync"}[s.Mode]
	if method == "" {
		ve.Add("mode", "unsupported mode")
	}
	if s.Mode != engine.ModeBisync {
		if o.Bisync != nil {
			ve.Add("options.bisync", "only valid in bisync mode")
		}
		if s.Resync {
			ve.Add("resync", "only valid in bisync mode")
		}
	}
	src, err := BuildFs(s.Source, e.Paths, e.Client)
	if err != nil {
		ve.Add("source", err.Error())
	}
	dst, err := BuildFs(s.Destination, e.Paths, e.Client)
	if err != nil {
		ve.Add("destination", err.Error())
	}
	if s.Source.Type == engine.EndpointLocal && s.Destination.Type == engine.EndpointLocal && src != "" && dst != "" {
		if dst == src || within(dst, src) {
			ve.Add("destination.path", "destination must not be inside the source")
		}
		if s.Mode == engine.ModeBisync && within(src, dst) {
			ve.Add("source.path", "source must not be inside the destination")
		}
	}
	if s.Mode == engine.ModeBisync {
		b := e.buildBisync(s, o, src, dst, ve)
		if err := ve.Err(); err != nil {
			return built{}, err
		}
		return b, nil
	}
	if err := ve.Err(); err != nil {
		return built{}, err
	}
	cfg := map[string]any{"DryRun": s.DryRun, "LogLevel": "INFO"}
	if o.Transfers != nil {
		cfg["Transfers"] = *o.Transfers
	}
	if o.Checkers != nil {
		cfg["Checkers"] = *o.Checkers
	}
	if o.MaxDelete != nil {
		cfg["MaxDelete"] = *o.MaxDelete
	}
	for k, v := range map[string]bool{"CheckSum": o.Checksum, "SizeOnly": o.SizeOnly, "IgnoreExisting": o.IgnoreExisting,
		"UpdateOlder": o.Update, "TrackRenames": o.TrackRenames} {
		if v {
			cfg[k] = true
		}
	}
	filter := map[string]any{}
	var rules []string
	for _, p := range o.Includes {
		rules = append(rules, "+ "+p)
	}
	for _, p := range o.Excludes {
		rules = append(rules, "- "+p)
	}
	if len(rules) > 0 {
		filter["FilterRule"] = rules
	}
	for k, v := range map[string]string{"MaxAge": o.MaxAge, "MinAge": o.MinAge, "MaxSize": o.MaxSize, "MinSize": o.MinSize} {
		if v != "" {
			filter[k] = v
		}
	}
	if o.DeleteExcluded {
		filter["DeleteExcluded"] = true
	}
	in := map[string]any{"srcFs": src, "dstFs": dst, "createEmptySrcDirs": o.CreateEmptySrcDirs, "_config": cfg}
	if s.Mode == engine.ModeMove {
		in["deleteEmptySrcDirs"] = o.DeleteEmptySrcDirs
	}
	if len(filter) > 0 {
		in["_filter"] = filter
	}
	return built{method: method, in: in}, nil
}

// Validate implémente engine.Engine.
func (e *Engine) Validate(_ context.Context, s engine.Spec) error {
	_, err := e.build(s)
	return err
}

// Preview implémente engine.Engine.
func (e *Engine) Preview(s engine.Spec) (engine.Preview, error) {
	b, err := e.build(s)
	if err != nil {
		return engine.Preview{}, err
	}
	return engine.Preview{RPC: b.method, In: b.in}, nil
}

// Start implémente engine.Engine.
func (e *Engine) Start(_ context.Context, s engine.Spec, sink engine.Sink) (engine.Execution, error) {
	b, err := e.build(s)
	if err != nil {
		return nil, err
	}
	for p, content := range b.files {
		if err := writeFileIfChanged(p, content); err != nil {
			return nil, fmt.Errorf("rclone: %w", err)
		}
	}
	group := "run/" + s.RunID
	b.in["_group"] = group
	e.Client.logs.register(group, sink)
	jobID, err := e.Client.startAsync(b.method, b.in)
	if err != nil {
		e.Client.logs.unregister(group)
		return nil, err
	}
	x := &execution{c: e.Client, jobID: jobID, group: group, sink: sink, done: make(chan struct{}),
		poll: e.PollInterval, stopTimeout: e.StopTimeout, bisync: s.Mode == engine.ModeBisync}
	if x.poll <= 0 {
		x.poll = time.Second
	}
	if x.stopTimeout <= 0 {
		x.stopTimeout = 30 * time.Second
	}
	sink.OnLog(engine.LogLine{Time: time.Now(), Stream: "system", Level: engine.LevelInfo,
		Msg: fmt.Sprintf("rclone %s started (job %d)", b.method, jobID)})
	go x.run()
	return x, nil
}

type execution struct {
	c           *Client
	jobID       int64
	group       string
	sink        engine.Sink
	poll        time.Duration
	stopTimeout time.Duration
	bisync      bool

	mu        sync.Mutex
	progress  engine.Progress
	result    engine.Result
	cancelled bool
	cancelAt  time.Time
	lastErr   string
	once      sync.Once
	done      chan struct{}
}

func (x *execution) PID() int              { return 0 }
func (x *execution) ExternalID() int64     { return x.jobID }
func (x *execution) Done() <-chan struct{} { return x.done }

func (x *execution) Progress() engine.Progress {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.progress
}

func (x *execution) Result() engine.Result {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.result
}

func (x *execution) Cancel(reason string) {
	x.once.Do(func() {
		x.mu.Lock()
		x.cancelled = true
		x.cancelAt = time.Now()
		x.mu.Unlock()
		x.sink.OnLog(engine.LogLine{Time: time.Now(), Stream: "system", Level: engine.LevelWarn, Msg: "cancel requested: " + reason})
		if err := x.c.JobStop(x.jobID); err != nil {
			x.sink.OnLog(engine.LogLine{Time: time.Now(), Stream: "system", Level: engine.LevelError, Msg: "job/stop: " + err.Error()})
		}
	})
}

func toProgress(st Stats) engine.Progress {
	p := engine.Progress{Phase: engine.PhaseTransferring, BytesDone: st.Bytes, BytesTotal: st.TotalBytes,
		FilesDone: st.Transfers, FilesTotal: st.TotalTransfers, Deletes: st.Deletes, Errors: st.Errors, SpeedBps: st.Speed}
	if st.TotalTransfers == 0 && st.TotalChecks == 0 {
		p.Phase = engine.PhaseScanning
	}
	if st.ETA != nil {
		v := int64(*st.ETA)
		p.ETASec = &v
	}
	for _, t := range st.Transferring {
		p.Current = append(p.Current, engine.FileProgress{Name: t.Name, Size: t.Size, Percent: t.Percentage})
	}
	return p
}

func (x *execution) pollStats() {
	st, err := x.c.Stats(x.group)
	if err != nil {
		return
	}
	p := toProgress(st)
	x.mu.Lock()
	x.progress = p
	newErr := st.LastError != "" && st.LastError != x.lastErr
	if newErr {
		x.lastErr = st.LastError
	}
	x.mu.Unlock()
	x.sink.OnProgress(p)
	if newErr {
		x.sink.OnLog(engine.LogLine{Time: time.Now(), Stream: "rclone", Level: engine.LevelError, Msg: Redact(st.LastError)})
	}
}

func (x *execution) run() {
	defer close(x.done)
	defer x.c.logs.unregister(x.group)
	defer x.c.StatsDelete(x.group)
	t := time.NewTicker(x.poll)
	defer t.Stop()
	var js JobStatus
	for {
		var err error
		js, err = x.c.JobStatus(x.jobID)
		if err != nil {
			x.finish(engine.Result{Outcome: engine.OutcomeFailed, Err: fmt.Errorf("job/status: %w", err)})
			return
		}
		x.pollStats()
		if js.Finished {
			break
		}
		x.mu.Lock()
		stuck := x.cancelled && time.Since(x.cancelAt) > x.stopTimeout
		x.mu.Unlock()
		if stuck {
			x.finish(engine.Result{Outcome: engine.OutcomeFailed, Err: fmt.Errorf("cancellation not confirmed by rclone")})
			return
		}
		<-t.C
	}
	st, _ := x.c.Stats(x.group)
	final := toProgress(st)
	final.Phase, final.Current, final.ETASec = engine.PhaseFinalizing, nil, nil
	final.FilesTotal = max(final.FilesTotal, final.FilesDone)
	final.TotalsExact = true
	x.mu.Lock()
	cancelled := x.cancelled
	x.mu.Unlock()
	if x.bisync {
		for _, l := range bisyncOutputLines(js.Output) {
			l.Time, l.Msg = time.Now(), Redact(l.Msg)
			x.sink.OnLog(l)
		}
	}
	res := engine.Result{Final: final}
	switch {
	case cancelled:
		res.Outcome = engine.OutcomeCancelled
	case js.Success && st.Errors == 0:
		res.Outcome = engine.OutcomeSucceeded
	case !st.FatalError && st.Errors > 0 && (st.Transfers > 0 || st.Checks > 0):
		res.Outcome = engine.OutcomePartial
		res.Err = fmt.Errorf("%d error(s): %s", st.Errors, Redact(firstNonEmpty(js.Error, st.LastError)))
	default:
		res.Outcome = engine.OutcomeFailed
		res.Err = fmt.Errorf("%s", Redact(firstNonEmpty(js.Error, st.LastError, "rclone job failed")))
		res.Retryable = !st.FatalError
		if x.bisync && bisyncNeedsResync(js.Error+"\n"+outputText(js.Output)) {
			// Relancer à l'identique échouerait de nouveau : seul un resync,
			// décidé par un humain, peut débloquer la situation.
			res.Retryable = false
			x.sink.OnLog(engine.LogLine{Time: time.Now(), Stream: "system", Level: engine.LevelError,
				Msg: "bisync needs a resync: run the job again with the resync option (it never deletes files)"})
		}
	}
	x.finish(res)
}

func (x *execution) finish(r engine.Result) {
	x.mu.Lock()
	x.result = r
	x.mu.Unlock()
	x.sink.OnProgress(r.Final)
}

// within indique si child est strictement sous parent (chemins locaux résolus).
func within(child, parent string) bool {
	sep := string(filepath.Separator)
	return strings.HasPrefix(child, strings.TrimSuffix(parent, sep)+sep)
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
