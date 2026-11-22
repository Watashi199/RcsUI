// Package api expose l'API HTTP /api/v1 et sert la SPA embarquée.
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"regexp"
	"runtime/debug"
	"strings"

	"rcsui/internal/auth"
	"rcsui/internal/engine"
	"rcsui/internal/engine/rclone"
	"rcsui/internal/events"
	"rcsui/internal/notify"
	"rcsui/internal/paths"
	"rcsui/internal/runner"
	"rcsui/internal/scheduler"
	"rcsui/internal/sshkeys"
	"rcsui/internal/store"
)

// Server regroupe les dépendances de l'API.
type Server struct {
	DB        *store.DB
	Sessions  *auth.Sessions
	Limiter   *auth.Limiter
	Runner    *runner.Manager
	Scheduler *scheduler.Scheduler
	Broker    *events.Broker
	Paths     *paths.Resolver
	Rclone    *rclone.Client   // nil si rclone est désactivé
	SSH       *sshkeys.Manager // nil si ssh est indisponible
	OIDC      *auth.OIDC       // nil si OIDC n'est pas configuré
	Notifier  *notify.Notifier // nil : notifications indisponibles
	Metrics   http.Handler     // nil : /metrics non monté (RCSUI_METRICS_TOKEN absent)
	LogDir    string
	Static    fs.FS // build SvelteKit (nil en développement)
	BaseURL   string
	Log       *slog.Logger
	Info      SystemInfo

	csp string
}

// SystemInfo est renvoyé par /system/info.
type SystemInfo struct {
	Version       string   `json:"version"`
	RcloneVersion string   `json:"rcloneVersion,omitempty"`
	RsyncVersion  string   `json:"rsyncVersion,omitempty"`
	AllowedRoots  []string `json:"allowedRoots"`
	MaxConcurrent int      `json:"maxConcurrent"`
	RcloneEnabled bool     `json:"rcloneEnabled"`
	SSHAvailable  bool     `json:"sshAvailable"`
	SSHMounted    bool     `json:"sshMountedAvailable"`
	OIDCEnabled   bool     `json:"oidcEnabled"`
}

const maxBody = 1 << 20

// Handler construit le routeur complet.
func (s *Server) Handler() http.Handler {
	if s.Log == nil {
		s.Log = slog.Default()
	}
	s.csp = s.buildCSP()
	mux := http.NewServeMux()
	a := func(pattern string, h authedHandler) { mux.Handle(pattern, s.authed(h)) }

	mux.HandleFunc("GET /api/v1/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("GET /api/v1/auth/config", s.handleAuthConfig)
	mux.HandleFunc("GET /api/v1/auth/oidc/login", s.handleOIDCLogin)
	mux.HandleFunc("GET /api/v1/auth/oidc/callback", s.handleOIDCCallback)
	a("POST /api/v1/auth/logout", s.handleLogout)
	a("GET /api/v1/auth/me", s.handleMe)
	a("POST /api/v1/auth/password", s.handleChangePassword)

	a("GET /api/v1/system/info", s.handleSystemInfo)
	a("GET /api/v1/settings", s.adminOnly(s.handleGetSettings))
	a("PUT /api/v1/settings", s.adminOnly(s.handlePutSettings))
	a("GET /api/v1/settings/notifications", s.adminOnly(s.handleGetNotifications))
	a("PUT /api/v1/settings/notifications", s.adminOnly(s.handlePutNotifications))
	a("POST /api/v1/settings/notifications/test", s.adminOnly(s.handleTestNotifications))
	if s.Metrics != nil {
		// Hors session : authentifié par jeton bearer (scrape Prometheus).
		mux.Handle("GET /metrics", s.Metrics)
	}
	a("GET /api/v1/users", s.adminOnly(s.handleListUsers))
	a("POST /api/v1/users", s.adminOnly(s.handleCreateUser))
	a("PATCH /api/v1/users/{id}", s.adminOnly(s.handlePatchUser))
	a("DELETE /api/v1/users/{id}", s.adminOnly(s.handleDeleteUser))
	a("GET /api/v1/audit", s.adminOnly(s.handleAudit))

	a("GET /api/v1/jobs", s.handleListJobs)
	a("POST /api/v1/jobs", s.handleCreateJob)
	a("POST /api/v1/jobs/validate", s.handleValidateJob)
	a("GET /api/v1/jobs/{id}", s.handleGetJob)
	a("PUT /api/v1/jobs/{id}", s.handleUpdateJob)
	a("DELETE /api/v1/jobs/{id}", s.handleDeleteJob)
	a("POST /api/v1/jobs/{id}/run", s.handleRunJob)
	a("GET /api/v1/jobs/{id}/grants", s.handleGetJobGrants)
	a("PUT /api/v1/jobs/{id}/grants", s.handlePutJobGrants)
	a("GET /api/v1/schedule/preview", s.handleSchedulePreview)

	a("GET /api/v1/runs", s.handleListRuns)
	a("GET /api/v1/runs/{id}", s.handleGetRun)
	a("POST /api/v1/runs/{id}/cancel", s.handleCancelRun)
	a("POST /api/v1/runs/{id}/retry", s.handleRetryRun)
	a("GET /api/v1/runs/{id}/logs", s.handleRunLogs)
	a("GET /api/v1/runs/{id}/logs.ndjson", s.handleRunLogsDownload)
	a("GET /api/v1/runs/{id}/items", s.handleRunItems)
	a("GET /api/v1/events", s.handleGlobalEvents)
	a("GET /api/v1/runs/{id}/events", s.handleRunEvents)

	a("GET /api/v1/rclone/providers", s.handleProviders)
	a("GET /api/v1/remotes", s.handleListRemotes)
	a("POST /api/v1/remotes", s.adminOnly(s.handleCreateRemote))
	a("GET /api/v1/remotes/{name}", s.handleGetRemote)
	a("PUT /api/v1/remotes/{name}", s.adminOnly(s.handleUpdateRemote))
	a("DELETE /api/v1/remotes/{name}", s.adminOnly(s.handleDeleteRemote))
	a("POST /api/v1/remotes/{name}/continue", s.adminOnly(s.handleContinueRemote))
	a("POST /api/v1/remotes/{name}/test", s.handleTestRemote)
	a("GET /api/v1/remotes/{name}/grants", s.adminOnly(s.handleGetRemoteGrants))
	a("PUT /api/v1/remotes/{name}/grants", s.adminOnly(s.handlePutRemoteGrants))

	a("GET /api/v1/ssh/identities", s.handleListIdentities)
	a("POST /api/v1/ssh/identities", s.adminOnly(s.handleCreateIdentity))
	a("DELETE /api/v1/ssh/identities/{id}", s.adminOnly(s.handleDeleteIdentity))
	a("GET /api/v1/ssh/identities/{id}/grants", s.adminOnly(s.handleGetIdentityGrants))
	a("PUT /api/v1/ssh/identities/{id}/grants", s.adminOnly(s.handlePutIdentityGrants))
	a("GET /api/v1/ssh/known-hosts", s.handleListKnownHosts)
	a("POST /api/v1/ssh/known-hosts/scan", s.adminOnly(s.handleScanHost))
	a("POST /api/v1/ssh/known-hosts", s.adminOnly(s.handleApproveHost))
	a("DELETE /api/v1/ssh/known-hosts/{id}", s.adminOnly(s.handleDeleteKnownHost))
	a("POST /api/v1/ssh/test", s.handleTestSSH)

	a("GET /api/v1/browse/roots", s.handleBrowseRoots)
	a("GET /api/v1/browse", s.handleBrowse)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "unknown endpoint")
	})
	mux.HandleFunc("/", s.handleStatic)

	// Première couche CSRF : rejet des requêtes cross-origin non sûres
	// (Sec-Fetch-Site / Origin). La seconde couche est le token X-CSRF-Token.
	cop := http.NewCrossOriginProtection()
	if s.BaseURL != "" {
		if err := cop.AddTrustedOrigin(s.BaseURL); err != nil {
			s.Log.Warn("invalid RCSUI_BASE_URL for origin check", "err", err)
		}
	}
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusForbidden, "cross_origin", "cross-origin request rejected")
	}))
	return s.recoverer(s.securityHeaders(s.limitBody(cop.Handler(mux))))
}

// --- middlewares -----------------------------------------------------------

type authedHandler func(w http.ResponseWriter, r *http.Request, cur *auth.Current)

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// authed exige une session valide, le token CSRF pour les méthodes non sûres,
// et bloque tout sauf le changement de mot de passe si celui-ci est requis.
func (s *Server) authed(h authedHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur, err := s.Sessions.Lookup(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
			return
		}
		if !isSafeMethod(r.Method) && !auth.CheckCSRF(r, cur) {
			writeError(w, http.StatusForbidden, "csrf", "missing or invalid CSRF token")
			return
		}
		if cur.User.MustChangePW {
			switch r.URL.Path {
			case "/api/v1/auth/me", "/api/v1/auth/password", "/api/v1/auth/logout":
			default:
				writeError(w, http.StatusForbidden, "password_change_required", "you must change your password first")
				return
			}
		}
		h(w, r.WithContext(auth.WithCurrent(r.Context(), cur)), cur)
	})
}

func (s *Server) adminOnly(h authedHandler) authedHandler {
	return func(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
		if cur.User.Role != store.RoleAdmin {
			writeError(w, http.StatusForbidden, "forbidden", "administrator role required")
			return
		}
		h(w, r, cur)
	}
}

func (s *Server) limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", s.csp)
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.Log.Error("panic in handler", "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal", "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// --- helpers ---------------------------------------------------------------

type apiError struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: msg}})
}

var errBadRequest = errors.New("bad request")

type badRequestError struct{ msg string }

func (e badRequestError) Error() string   { return e.msg }
func (e badRequestError) Is(t error) bool { return t == errBadRequest }

func badRequest(msg string) error { return badRequestError{msg} }

// fail traduit une erreur en réponse HTTP.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ve *engine.ValidationError
	var mbe *http.MaxBytesError
	var re *rclone.RPCError
	switch {
	case errors.As(err, &ve):
		writeJSON(w, http.StatusBadRequest, map[string]apiError{"error": {Code: "validation", Message: ve.Error(), Fields: ve.Fields}})
	case errors.As(err, &mbe):
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "request body too large")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "already exists")
	case errors.Is(err, runner.ErrJobActive):
		writeError(w, http.StatusConflict, "job_active", err.Error())
	case errors.Is(err, runner.ErrDestinationBusy):
		writeError(w, http.StatusConflict, "destination_busy", err.Error())
	case errors.Is(err, runner.ErrNotActive):
		writeError(w, http.StatusConflict, "run_not_active", err.Error())
	case errors.Is(err, runner.ErrShuttingDown):
		writeError(w, http.StatusServiceUnavailable, "shutting_down", err.Error())
	case errors.Is(err, paths.ErrOutsideRoots), errors.Is(err, paths.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_path", err.Error())
	case errors.Is(err, errBadRequest):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.As(err, &re):
		writeError(w, http.StatusBadGateway, "rclone", re.Error())
	case errors.Is(err, context.Canceled):
		// client parti
	default:
		s.Log.Error("request failed", "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

// decode lit un corps JSON strict (champs inconnus refusés).
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return err
		}
		return badRequest("invalid JSON: " + err.Error())
	}
	if dec.More() {
		return badRequest("unexpected data after JSON body")
	}
	return nil
}

func (s *Server) audit(r *http.Request, cur *auth.Current, action, target string, details map[string]any) {
	var uid *int64
	if cur != nil {
		id := cur.User.ID
		uid = &id
	}
	if err := s.DB.AddAudit(r.Context(), uid, action, target, details); err != nil {
		s.Log.Warn("audit write failed", "err", err)
	}
}

// --- SPA statique ------------------------------------------------------------

var inlineScriptRe = regexp.MustCompile(`(?s)<script(?:\s[^>]*)?>(.*?)</script>`)

// buildCSP calcule les hashes des scripts inline de la page SPA : CSP stricte
// sans 'unsafe-inline' pour les scripts.
func (s *Server) buildCSP() string {
	scripts := []string{"'self'"}
	if s.Static != nil {
		for _, name := range []string{"200.html", "index.html"} {
			b, err := fs.ReadFile(s.Static, name)
			if err != nil {
				continue
			}
			for _, m := range inlineScriptRe.FindAllSubmatch(b, -1) {
				if len(bytes.TrimSpace(m[1])) == 0 {
					continue
				}
				sum := sha256.Sum256(m[1])
				scripts = append(scripts, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
			}
		}
	}
	return "default-src 'self'; script-src " + strings.Join(scripts, " ") +
		"; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self'; " +
		"object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if s.Static == nil {
		http.Error(w, "frontend not built", http.StatusNotFound)
		return
	}
	p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if p != "" {
		if st, err := fs.Stat(s.Static, p); err == nil && !st.IsDir() {
			if strings.HasPrefix(p, "_app/immutable/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			serveFile(w, r, s.Static, p)
			return
		}
	}
	// Fallback SPA : toute route inconnue rend la page d'amorçage.
	w.Header().Set("Cache-Control", "no-cache")
	for _, name := range []string{"200.html", "index.html"} {
		if _, err := fs.Stat(s.Static, name); err == nil {
			serveFile(w, r, s.Static, name)
			return
		}
	}
	http.NotFound(w, r)
}

func serveFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) {
	f, err := fsys.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, name, st.ModTime(), rs)
		return
	}
	_, _ = io.Copy(w, f)
}
