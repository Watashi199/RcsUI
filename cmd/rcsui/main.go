// Command rcsui est le serveur RcsUI : UI web pour piloter rclone et rsync.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"rcsui/internal/api"
	"rcsui/internal/auth"
	"rcsui/internal/config"
	"rcsui/internal/engine"
	"rcsui/internal/engine/rclone"
	"rcsui/internal/engine/rsync"
	"rcsui/internal/events"
	"rcsui/internal/metrics"
	"rcsui/internal/notify"
	"rcsui/internal/paths"
	"rcsui/internal/runner"
	"rcsui/internal/scheduler"
	"rcsui/internal/secrets"
	"rcsui/internal/sshkeys"
	"rcsui/internal/store"
	"rcsui/web"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if len(os.Args) > 1 && os.Args[1] == "rclone-config-password" {
		os.Exit(printRclonePassword())
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func newLogger(level string) *slog.Logger {
	l := slog.LevelInfo
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)
	log.Info("starting rcsui", "version", version, "data", cfg.DataDir)

	for _, d := range []string{cfg.DataDir, cfg.LogDir(), cfg.BackupDir(), cfg.RunTmpDir(), cfg.SSHStateDir(), filepath.Join(cfg.DataDir, "home")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}

	ctx := context.Background()
	db, err := store.Open(ctx, cfg.DBPath(), cfg.BackupDir())
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer db.Close()
	if err := bootstrapAdmin(ctx, db, cfg, log); err != nil {
		return err
	}

	resolver, err := paths.New(cfg.AllowedRoots)
	if err != nil {
		return fmt.Errorf("RCSUI_ALLOWED_ROOTS: %w", err)
	}
	if len(cfg.AllowedRoots) == 0 {
		log.Warn("RCSUI_ALLOWED_ROOTS is empty: no local path can be used")
	}

	key, err := secrets.LoadOrCreate(cfg.SecretKey, filepath.Join(cfg.DataDir, "secret.key"))
	if err != nil {
		return fmt.Errorf("secret key: %w", err)
	}
	box, err := secrets.New(key)
	if err != nil {
		return err
	}

	info := api.SystemInfo{Version: version, AllowedRoots: resolver.Roots(), RsyncVersion: rsyncVersion(cfg.RsyncBinary)}
	var sshMgr *sshkeys.Manager
	if sshkeys.SSHBinaryAvailable() {
		sshMgr = &sshkeys.Manager{DB: db, Box: box, StateDir: cfg.SSHStateDir(), RunDir: filepath.Join(cfg.RunTmpDir(), "ssh"), MountedDir: cfg.SSHDir}
		if err := sshMgr.WriteKnownHosts(ctx); err != nil {
			return fmt.Errorf("known_hosts: %w", err)
		}
		info.SSHAvailable = true
		info.SSHMounted = sshMgr.MountedAvailable()
		if cfg.SSHDir != "" && !info.SSHMounted {
			log.Warn("RCSUI_SSH_DIR ignored: it must be an existing directory named .ssh", "path", cfg.SSHDir)
		}
	} else {
		log.Warn("ssh client not found: rsync over SSH disabled")
	}
	engines := map[engine.Kind]engine.Engine{}
	if info.RsyncVersion != "" {
		re := &rsync.Engine{
			Paths:  resolver,
			Binary: cfg.RsyncBinary,
			Home:   filepath.Join(cfg.DataDir, "home"),
			VanishedOK: func() bool {
				return db.GetBoolSetting(context.Background(), api.SettingVanishedSuccess, true)
			},
		}
		if sshMgr != nil {
			re.SSH = sshMgr
		}
		engines[engine.KindRsync] = re
	} else {
		log.Warn("rsync binary not found: rsync engine disabled", "binary", cfg.RsyncBinary)
	}
	var rc *rclone.Client
	if !cfg.DisableRclone {
		pass, err := secrets.RcloneConfigPassword(key)
		if err != nil {
			return err
		}
		rc, err = rclone.Init(cfg.RcloneConfig(), cfg.RcloneCache(),
			rclone.ConfigCrypt{Password: pass, Encrypt: cfg.RcloneEncrypt, BackupDir: cfg.BackupDir()})
		if err != nil {
			return fmt.Errorf("rclone: %w", err)
		}
		defer rclone.Finalize()
		switch {
		case rc.Crypt.Converted && rc.Crypt.Encrypted && rc.Crypt.Backup != "":
			log.Warn("rclone.conf is now encrypted with a key derived from the master key; "+
				"delete the plain-text backup once remotes are checked", "backup", rc.Crypt.Backup)
		case rc.Crypt.Converted && rc.Crypt.Encrypted:
			// Installation neuve : fichier vide, aucune copie en clair.
			log.Info("rclone.conf encrypted with a key derived from the master key")
		case rc.Crypt.Converted:
			log.Warn("rclone.conf decrypted (RCSUI_RCLONE_CONFIG_ENCRYPT=false)", "backup", rc.Crypt.Backup)
		case !rc.Crypt.Encrypted:
			log.Warn("rclone.conf is not encrypted (RCSUI_RCLONE_CONFIG_ENCRYPT=false)")
		}
		// Listings de référence de bisync sur le volume : ils doivent survivre
		// aux redémarrages, sinon chaque run exigerait un resync.
		engines[engine.KindRclone] = &rclone.Engine{Client: rc, Paths: resolver, BisyncDir: filepath.Join(cfg.DataDir, "bisync")}
		info.RcloneEnabled = true
		info.RcloneVersion = rc.Version()
	}

	notifier := &notify.Notifier{DB: db, Box: box, BaseURL: cfg.BaseURL, Log: log}
	if err := notifier.Load(ctx); err != nil {
		return fmt.Errorf("notifications: %w", err)
	}
	met := &metrics.Metrics{DB: db, Log: log,
		Build: metrics.BuildInfo{Version: version, RcloneVersion: info.RcloneVersion, RsyncVersion: info.RsyncVersion}}

	broker := events.NewBroker()
	maxConc := db.GetIntSetting(ctx, api.SettingMaxConcurrent, cfg.MaxConcurrent)
	mgr := runner.New(runner.Deps{DB: db, Engines: engines, Broker: broker, LogDir: cfg.LogDir(), Log: log,
		OnFinished: []func(runner.Finished){
			func(f runner.Finished) { met.RunFinished(f.Run) },
			func(f runner.Finished) { notifier.RunFinished(f.Run, f.RetryAt) },
		}}, maxConc)
	met.Runner = mgr
	// Avant Recover et RestoreRetries : les runs qu'ils relancent écrivent leurs
	// fichiers SSH dans ce répertoire (une relance échue démarre immédiatement).
	cleanRunTmp(cfg.RunTmpDir(), log)
	n, err := mgr.Recover(ctx, nil)
	if err != nil {
		return fmt.Errorf("recover runs: %w", err)
	}
	if n > 0 {
		log.Warn("runs interrupted by previous shutdown", "count", n)
	}
	if n, err := mgr.RestoreRetries(ctx); err != nil {
		return fmt.Errorf("restore pending retries: %w", err)
	} else if n > 0 {
		log.Info("automatic retries restored", "count", n)
	}

	sched := scheduler.New(mgr, db, time.Local, log)
	if err := sched.Start(ctx); err != nil {
		return fmt.Errorf("scheduler: %w", err)
	}

	var oidcAuth *auth.OIDC
	if cfg.OIDC.Issuer != "" {
		o := cfg.OIDC
		dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		oidcAuth, err = auth.NewOIDC(dctx, auth.OIDCConfig{Issuer: o.Issuer, ClientID: o.ClientID, ClientSecret: o.ClientSecret,
			RedirectURL: cfg.BaseURL + "/api/v1/auth/oidc/callback", Scopes: o.Scopes, RoleClaim: o.RoleClaim,
			UsernameClaim: o.UsernameClaim, AdminGroups: o.AdminGroups, OperatorGroups: o.OperatorGroups, AutoProvision: o.AutoProvision})
		cancel()
		if err != nil {
			return err
		}
		info.OIDCEnabled = true
		log.Info("oidc enabled", "issuer", o.Issuer, "autoProvision", o.AutoProvision)
	}

	var static fs.FS
	if sub, err := web.Build(); err == nil {
		static = sub
	} else {
		log.Warn("frontend not embedded", "err", err)
	}

	srv := &api.Server{
		DB:        db,
		Sessions:  &auth.Sessions{DB: db, Idle: cfg.SessionIdle, Absolute: cfg.SessionAbsolute, CookieSecure: cfg.CookieSecure, TrustedProxies: cfg.TrustedProxies},
		Limiter:   auth.NewLimiter(),
		Runner:    mgr,
		Scheduler: sched,
		Broker:    broker,
		Paths:     resolver,
		Rclone:    rc,
		SSH:       sshMgr,
		OIDC:      oidcAuth,
		Notifier:  notifier,
		LogDir:    cfg.LogDir(),
		Static:    static,
		BaseURL:   cfg.BaseURL,
		Log:       log,
		Info:      info,
	}

	if cfg.MetricsToken != "" {
		srv.Metrics = met.Handler(cfg.MetricsToken)
		log.Info("prometheus metrics enabled", "path", "/metrics")
	}

	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second, // les flux SSE repoussent leur deadline à chaque écriture
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	stopMaint := make(chan struct{})
	go maintenance(db, cfg, log, stopMaint)

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Listen)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		log.Info("shutting down", "signal", s.String())
	case err := <-errCh:
		if err != nil {
			return err
		}
	}

	// Ordre : planification, puis HTTP (les flux SSE se terminent via baseCtx), puis runs.
	sched.Stop()
	close(stopMaint)
	cancelBase()
	shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = httpSrv.Shutdown(shCtx)
	cancel()
	runCtx, cancel2 := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel2()
	if err := mgr.Shutdown(runCtx); err != nil {
		log.Warn("some runs did not stop in time; they will be marked interrupted at next start", "err", err)
	}
	// Après le runner : les notifications de fin des runs arrêtés partent encore.
	ntCtx, cancel3 := context.WithTimeout(context.Background(), 5*time.Second)
	notifier.Stop(ntCtx)
	cancel3()
	log.Info("bye")
	return nil
}

// bootstrapAdmin crée le premier administrateur. Sans RCSUI_ADMIN_PASSWORD, un
// mot de passe aléatoire est affiché UNE fois et devra être changé au premier login.
func bootstrapAdmin(ctx context.Context, db *store.DB, cfg config.Config, log *slog.Logger) error {
	n, err := db.CountUsers(ctx)
	if err != nil || n > 0 {
		return err
	}
	pw := cfg.AdminPassword
	generated := pw == ""
	if generated {
		pw = auth.RandomPassword(20)
	} else if err := auth.CheckPasswordPolicy(pw); err != nil {
		return fmt.Errorf("RCSUI_ADMIN_PASSWORD: %w", err)
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	if _, err := db.CreateUser(ctx, store.User{Username: cfg.AdminUser, PasswordHash: h, Role: store.RoleAdmin, MustChangePW: generated}); err != nil {
		return fmt.Errorf("create admin: %w", err)
	}
	if generated {
		// Volontairement hors du logger structuré, et une seule fois.
		fmt.Fprintf(os.Stderr, "\n================ RcsUI ================\n"+
			" Initial administrator account created\n user:     %s\n password: %s\n"+
			" You will be asked to change it at first login.\n=======================================\n\n", cfg.AdminUser, pw)
	} else {
		log.Info("initial administrator created from RCSUI_ADMIN_PASSWORD", "user", cfg.AdminUser)
	}
	return nil
}

func rsyncVersion(bin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(string(out), "\n")
	f := strings.Fields(first)
	for i, w := range f {
		if w == "version" && i+1 < len(f) {
			return f[i+1]
		}
	}
	return strings.TrimSpace(first)
}

// cleanRunTmp supprime les fichiers temporaires (clés SSH déchiffrées) laissés par un arrêt brutal.
func cleanRunTmp(dir string, log *slog.Logger) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			log.Warn("clean run tmp", "path", e.Name(), "err", err)
		}
	}
}

// maintenance purge périodiquement les sessions expirées et les vieux runs.
func maintenance(db *store.DB, cfg config.Config, log *slog.Logger, stop <-chan struct{}) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		ctx := context.Background()
		if _, err := db.PurgeSessions(ctx, time.Now(), cfg.SessionIdle); err != nil {
			log.Warn("purge sessions", "err", err)
		}
		days := db.GetIntSetting(ctx, api.SettingLogRetention, int(cfg.LogRetention.Hours()/24))
		files, err := db.PurgeRuns(ctx, time.Now().Add(-time.Duration(days)*24*time.Hour))
		if err != nil {
			log.Warn("purge runs", "err", err)
		}
		for _, rel := range files {
			base := filepath.Join(cfg.LogDir(), filepath.Clean(rel))
			_ = os.Remove(base)
			_ = os.Remove(strings.TrimSuffix(base, ".ndjson") + ".items.ndjson")
		}
		if len(files) > 0 {
			log.Info("purged old runs", "count", len(files))
		}
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

// healthcheck sert au HEALTHCHECK Docker (pas besoin de wget/curl dans l'image).
func healthcheck() int {
	addr := os.Getenv("RCSUI_LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort(host, port) + "/api/v1/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// printRclonePassword affiche le mot de passe de configuration rclone, pour
// utiliser rclone.conf avec la CLI rclone (RCLONE_CONFIG_PASS). Ne crée jamais
// de clé maître : sans clé existante, la configuration ne peut être chiffrée.
func printRclonePassword() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	path := filepath.Join(cfg.DataDir, "secret.key")
	if cfg.SecretKey == "" {
		if _, err := os.Stat(path); err != nil {
			fmt.Fprintln(os.Stderr, "no master key:", err)
			return 1
		}
	}
	key, err := secrets.LoadOrCreate(cfg.SecretKey, path)
	if err == nil {
		var pass string
		if pass, err = secrets.RcloneConfigPassword(key); err == nil {
			fmt.Println(pass)
			return 0
		}
	}
	fmt.Fprintln(os.Stderr, err)
	return 1
}
