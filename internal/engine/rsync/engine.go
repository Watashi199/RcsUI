package rsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rcsui/internal/engine"
	"rcsui/internal/paths"
)

// SSHProvider prépare les éléments SSH d'un run (clé déchiffrée, known_hosts).
type SSHProvider interface {
	// Check vérifie qu'une identité et un known_host existent pour la cible.
	Check(ctx context.Context, t *engine.SSHTarget) error
	// Prepare matérialise la configuration pour un run ; cleanup supprime les fichiers temporaires.
	Prepare(ctx context.Context, runID string, t *engine.SSHTarget) (cfg *SSHConfig, env []string, cleanup func(), err error)
}

// Engine est le moteur rsync (sous-processus).
type Engine struct {
	Paths      *paths.Resolver
	Binary     string        // "rsync" par défaut
	Home       string        // HOME du sous-processus
	SSH        SSHProvider   // nil : SSH indisponible
	VanishedOK func() bool   // code 24 considéré comme un succès
	KillGrace  time.Duration // délai SIGTERM -> SIGKILL (10 s par défaut)
	// ProgressInterval limite la fréquence des événements de progression.
	ProgressInterval time.Duration
}

var _ engine.Engine = (*Engine)(nil)

func (e *Engine) Kind() engine.Kind { return engine.KindRsync }

func (e *Engine) DecodeOptions(raw []byte) (any, error) { return DecodeOptions(raw) }

func (e *Engine) opts(s engine.Spec) (*Options, error) {
	o, ok := s.Options.(*Options)
	if !ok || o == nil {
		return nil, fmt.Errorf("rsync: options not decoded")
	}
	return o, nil
}

// resolve valide les endpoints et retourne les chemins locaux réels.
func (e *Engine) resolve(ctx context.Context, s engine.Spec, o *Options) (ResolvedPaths, error) {
	ve := &engine.ValidationError{}
	var rp ResolvedPaths
	if s.Mode != engine.ModeCopy && s.Mode != engine.ModeSync {
		ve.Add("mode", "rsync supports copy and sync")
	}
	rp.Source = e.checkEndpoint(ctx, ve, "source", s.Source)
	rp.Destination = e.checkEndpoint(ctx, ve, "destination", s.Destination)
	if s.Source.Type == engine.EndpointSSH && s.Destination.Type == engine.EndpointSSH {
		ve.Add("destination.type", "remote to remote is not supported")
	}
	if rp.Source != "" && rp.Destination != "" {
		// Destination égale à la source ou à l'intérieur : récursion infinie
		// (copy) ou suppression de données (sync). Refusé dans tous les modes.
		if strings.HasPrefix(rp.Destination+string(filepath.Separator), rp.Source+string(filepath.Separator)) {
			ve.Add("destination.path", "destination must not be inside the source")
		}
	}
	if o.BackupDir != "" {
		p, err := e.Paths.Resolve(o.BackupDir)
		if err != nil {
			ve.Add("options.backupDir", err.Error())
		}
		rp.BackupDir = p
	}
	if o.LinkDest != "" {
		p, err := e.Paths.Resolve(o.LinkDest)
		if err != nil {
			ve.Add("options.linkDest", err.Error())
		}
		rp.LinkDest = p
	}
	return rp, ve.Err()
}

// checkEndpoint valide un endpoint et retourne son chemin local réel ("" pour SSH).
func (e *Engine) checkEndpoint(ctx context.Context, ve *engine.ValidationError, field string, ep engine.Endpoint) string {
	switch ep.Type {
	case engine.EndpointLocal:
		p, err := e.Paths.Resolve(ep.Path)
		if err != nil {
			ve.Add(field+".path", err.Error())
		}
		return p
	case engine.EndpointSSH:
		e.checkSSHEndpoint(ctx, ve, field, ep)
	default:
		ve.Add(field+".type", "rsync supports local and ssh endpoints")
	}
	return ""
}

func (e *Engine) checkSSHEndpoint(ctx context.Context, ve *engine.ValidationError, field string, ep engine.Endpoint) {
	switch {
	case e.SSH == nil:
		ve.Add(field+".type", "ssh is not available")
	case ep.SSH == nil:
		ve.Add(field+".ssh", "required")
	default:
		if err := ValidateSSHTarget(ep.SSH); err != nil {
			ve.Add(field+".ssh", err.Error())
		} else if err := checkRemotePath(ep.Path); err != nil {
			ve.Add(field+".path", err.Error())
		} else if err := e.SSH.Check(ctx, ep.SSH); err != nil {
			ve.Add(field+".ssh", err.Error())
		}
	}
}

// Validate implémente engine.Engine.
func (e *Engine) Validate(ctx context.Context, s engine.Spec) error {
	if s.Resync {
		return &engine.ValidationError{Fields: map[string]string{"resync": "only valid for rclone bisync jobs"}}
	}
	o, err := e.opts(s)
	if err != nil {
		return err
	}
	_, err = e.resolve(ctx, s, o)
	return err
}

// Preview implémente engine.Engine (chemins SSH remplacés par des marqueurs).
func (e *Engine) Preview(s engine.Spec) (engine.Preview, error) {
	o, err := e.opts(s)
	if err != nil {
		return engine.Preview{}, err
	}
	rp, err := e.resolve(context.Background(), s, o)
	if err != nil {
		return engine.Preview{}, err
	}
	argv, err := BuildArgv(s, o, rp, &SSHConfig{KeyFile: "/data/run/ssh/RUN.key", KnownHostsFile: "/data/ssh/known_hosts", Mounted: sshMounted(s)})
	if err != nil {
		return engine.Preview{}, err
	}
	return engine.Preview{Argv: append([]string{e.binary()}, argv...)}, nil
}

func sshMounted(s engine.Spec) bool {
	if t := sshTarget(s); t != nil {
		return t.Identity.Mounted
	}
	return false
}

func (e *Engine) binary() string {
	if e.Binary == "" {
		return "rsync"
	}
	return e.Binary
}

// Start implémente engine.Engine.
func (e *Engine) Start(ctx context.Context, s engine.Spec, sink engine.Sink) (engine.Execution, error) {
	o, err := e.opts(s)
	if err != nil {
		return nil, err
	}
	rp, err := e.resolve(ctx, s, o)
	if err != nil {
		return nil, err
	}
	var sshCfg *SSHConfig
	var extraEnv []string
	cleanup := func() { /* rien à nettoyer sans SSH */ }
	if t := sshTarget(s); t != nil {
		sshCfg, extraEnv, cleanup, err = e.SSH.Prepare(ctx, s.RunID, t)
		if err != nil {
			return nil, fmt.Errorf("ssh: %w", err)
		}
	}
	argv, err := BuildArgv(s, o, rp, sshCfg)
	if err != nil {
		cleanup()
		return nil, err
	}

	cmd := exec.Command(e.binary(), argv...)
	home := e.Home
	if home == "" {
		home = os.TempDir()
	}
	// Environnement explicite et minimal : rien n'est hérité du serveur.
	cmd.Env = append([]string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + home,
		"LC_ALL=C.UTF-8",
		"RCSUI_RUN_ID=" + s.RunID,
	}, extraEnv...)
	setProcAttrs(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cleanup()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cleanup()
		return nil, err
	}
	x := &execution{
		cmd:      cmd,
		sink:     sink,
		tracker:  NewTracker(),
		done:     make(chan struct{}),
		cancelCh: make(chan struct{}),
		interval: e.ProgressInterval,
		grace:    e.KillGrace,
	}
	if x.interval <= 0 {
		x.interval = 500 * time.Millisecond
	}
	if x.grace <= 0 {
		x.grace = 10 * time.Second
	}
	vanishedOK := e.VanishedOK == nil || e.VanishedOK()
	sink.OnLog(engine.LogLine{Time: time.Now(), Stream: "system", Level: engine.LevelInfo,
		Msg: "exec " + strings.Join(append([]string{e.binary()}, argv...), " ")})
	if err := cmd.Start(); err != nil {
		cleanup()
		return nil, fmt.Errorf("start rsync: %w", err)
	}
	go x.run(stdout, stderr, vanishedOK, cleanup)
	return x, nil
}

type execution struct {
	cmd      *exec.Cmd
	sink     engine.Sink
	interval time.Duration
	grace    time.Duration

	mu         sync.Mutex
	tracker    *Tracker
	lastEmit   time.Time
	result     engine.Result
	cancelOnce sync.Once
	cancelled  bool
	cancelCh   chan struct{}
	done       chan struct{}
}

func (x *execution) PID() int          { return x.cmd.Process.Pid }
func (x *execution) ExternalID() int64 { return 0 }
func (x *execution) Done() <-chan struct{} {
	return x.done
}

func (x *execution) Progress() engine.Progress {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.tracker.Progress()
}

func (x *execution) Result() engine.Result {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.result
}

// Cancel envoie SIGTERM au groupe de processus, puis SIGKILL après le délai de grâce.
func (x *execution) Cancel(reason string) {
	x.cancelOnce.Do(func() {
		x.mu.Lock()
		x.cancelled = true
		x.mu.Unlock()
		close(x.cancelCh)
		x.sink.OnLog(engine.LogLine{Time: time.Now(), Stream: "system", Level: engine.LevelWarn, Msg: "cancel requested: " + reason})
		go func() {
			_ = terminateGroup(x.cmd.Process)
			select {
			case <-x.done:
			case <-time.After(x.grace):
				x.sink.OnLog(engine.LogLine{Time: time.Now(), Stream: "system", Level: engine.LevelWarn, Msg: "rsync did not exit after SIGTERM, sending SIGKILL"})
				_ = killGroup(x.cmd.Process)
			}
		}()
	})
}

func (x *execution) emitProgress(force bool) {
	x.mu.Lock()
	now := time.Now()
	if !force && now.Sub(x.lastEmit) < x.interval {
		x.mu.Unlock()
		return
	}
	x.lastEmit = now
	p := x.tracker.Progress()
	x.mu.Unlock()
	x.sink.OnProgress(p)
}

func (x *execution) readStdout(r io.Reader) {
	sc := NewScanner(r)
	for sc.Scan() {
		l := ParseLine(sc.Text())
		x.mu.Lock()
		changed := x.tracker.Apply(l)
		x.mu.Unlock()
		switch l.Kind {
		case LineItem:
			it := ToEngineItem(l.Item)
			it.Time = time.Now()
			x.sink.OnItem(it)
		case LineOther:
			x.sink.OnLog(engine.LogLine{Time: time.Now(), Stream: "stdout", Level: engine.LevelInfo, Msg: DecodeName(l.Text)})
		case LineStat:
			x.sink.OnLog(engine.LogLine{Time: time.Now(), Stream: "stdout", Level: engine.LevelInfo, Msg: l.Text})
		}
		if changed {
			x.emitProgress(false)
		}
	}
	if err := sc.Err(); err != nil {
		x.sink.OnLog(engine.LogLine{Time: time.Now(), Stream: "system", Level: engine.LevelError, Msg: "stdout read error: " + err.Error()})
		_, _ = io.Copy(io.Discard, r)
	}
}

func (x *execution) readStderr(r io.Reader, vanishedOK bool) {
	sc := NewScanner(r)
	for sc.Scan() {
		s := sc.Text()
		if strings.TrimSpace(s) == "" {
			continue
		}
		lvl := StderrLevel(s)
		isErr := strings.HasPrefix(s, "rsync:") || strings.HasPrefix(s, "rsync warning:")
		if strings.Contains(s, "file has vanished") && vanishedOK {
			isErr = false
		}
		if isErr {
			x.mu.Lock()
			x.tracker.AddError()
			x.mu.Unlock()
		}
		x.sink.OnLog(engine.LogLine{Time: time.Now(), Stream: "stderr", Level: lvl, Msg: DecodeName(s)})
	}
	if sc.Err() != nil {
		_, _ = io.Copy(io.Discard, r)
	}
}

func (x *execution) run(stdout, stderr io.Reader, vanishedOK bool, cleanup func()) {
	defer cleanup()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); x.readStdout(stdout) }()
	go func() { defer wg.Done(); x.readStderr(stderr, vanishedOK) }()
	wg.Wait()
	waitErr := x.cmd.Wait()

	code := -1
	var sigMsg string
	var exitErr *exec.ExitError
	switch {
	case waitErr == nil:
		code = 0
	case errors.As(waitErr, &exitErr):
		code = exitErr.ExitCode()
		if code == -1 {
			sigMsg = exitErr.String()
		}
	}

	x.mu.Lock()
	cancelled := x.cancelled
	final := x.tracker.Final()
	x.mu.Unlock()

	res := engine.Result{Final: final}
	switch {
	case code >= 0:
		info := InterpretExit(code, cancelled, vanishedOK)
		c := code
		res.ExitCode = &c
		res.Outcome, res.Retryable = info.Outcome, info.Retryable
		if info.Outcome != engine.OutcomeSucceeded && info.Outcome != engine.OutcomeCancelled {
			res.Err = fmt.Errorf("rsync exited with code %d: %s", code, info.Message)
		}
	case cancelled:
		res.Outcome = engine.OutcomeCancelled
	default:
		res.Outcome = engine.OutcomeFailed
		if sigMsg == "" && waitErr != nil {
			sigMsg = waitErr.Error()
		}
		res.Err = fmt.Errorf("rsync terminated abnormally: %s", sigMsg)
	}
	x.mu.Lock()
	x.result = res
	x.mu.Unlock()
	x.sink.OnProgress(final)
	close(x.done)
}
