package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"rcsui/internal/store"
)

// Sessions gère les sessions par cookie.
type Sessions struct {
	DB       *store.DB
	Idle     time.Duration // expiration par inactivité (24 h)
	Absolute time.Duration // durée de vie maximale (7 j)
	// CookieSecure : "auto" (TLS ou X-Forwarded-Proto d'un proxy de confiance), "true", "false".
	CookieSecure   string
	TrustedProxies []netip.Prefix
}

const (
	cookieSecureName   = "__Host-rcsui_session"
	cookieInsecureName = "rcsui_session"
	touchInterval      = time.Minute
)

// Current représente l'utilisateur authentifié d'une requête.
type Current struct {
	User      store.User
	TokenHash []byte
	CSRFToken string
}

type ctxKey struct{}

// FromContext retourne la session courante.
func FromContext(ctx context.Context) (*Current, bool) {
	c, ok := ctx.Value(ctxKey{}).(*Current)
	return c, ok
}

// WithCurrent attache la session au contexte.
func WithCurrent(ctx context.Context, c *Current) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

func hashToken(tok string) []byte {
	h := sha256.Sum256([]byte(tok))
	return h[:]
}

func (s *Sessions) fromTrustedProxy(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	for _, p := range s.TrustedProxies {
		if p.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

// IsHTTPS indique si la requête est servie en HTTPS (directement ou via un proxy de confiance).
func (s *Sessions) IsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return s.fromTrustedProxy(r) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// ClientIP retourne l'IP cliente ; X-Forwarded-For n'est lu que derrière un proxy de confiance.
func (s *Sessions) ClientIP(r *http.Request) string {
	if s.fromTrustedProxy(r) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Sessions) secure(r *http.Request) bool {
	switch s.CookieSecure {
	case "true":
		return true
	case "false":
		return false
	default:
		return s.IsHTTPS(r)
	}
}

func (s *Sessions) cookieName(r *http.Request) string {
	if s.secure(r) {
		return cookieSecureName
	}
	return cookieInsecureName
}

// Create ouvre une session (nouveau token : pas de fixation possible) et pose le cookie.
func (s *Sessions) Create(w http.ResponseWriter, r *http.Request, u store.User) (*Current, error) {
	tok := RandomToken(32)
	csrf := RandomToken(32)
	now := time.Now()
	sess := store.Session{TokenHash: hashToken(tok), UserID: u.ID, CSRFToken: csrf, CreatedAt: now, LastSeen: now,
		ExpiresAt: now.Add(s.Absolute), IP: s.ClientIP(r), UserAgent: truncate(r.UserAgent(), 256)}
	if err := s.DB.CreateSession(r.Context(), sess); err != nil {
		return nil, err
	}
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(r), Value: tok, Path: "/", HttpOnly: true,
		Secure: s.secure(r), SameSite: http.SameSiteLaxMode, Expires: sess.ExpiresAt})
	return &Current{User: u, TokenHash: sess.TokenHash, CSRFToken: csrf}, nil
}

// ErrNoSession : pas de session valide.
var ErrNoSession = errors.New("no valid session")

// Lookup valide le cookie de session.
func (s *Sessions) Lookup(r *http.Request) (*Current, error) {
	c, err := r.Cookie(s.cookieName(r))
	if err != nil || c.Value == "" || len(c.Value) > 128 {
		return nil, ErrNoSession
	}
	h := hashToken(c.Value)
	sess, u, err := s.DB.GetSession(r.Context(), h)
	if err != nil {
		return nil, ErrNoSession
	}
	now := time.Now()
	if now.After(sess.ExpiresAt) || now.Sub(sess.LastSeen) > s.Idle || u.Disabled {
		_ = s.DB.DeleteSession(r.Context(), h)
		return nil, ErrNoSession
	}
	if now.Sub(sess.LastSeen) > touchInterval {
		_ = s.DB.TouchSession(r.Context(), h, now)
	}
	return &Current{User: u, TokenHash: h, CSRFToken: sess.CSRFToken}, nil
}

// Destroy supprime la session courante et efface le cookie.
func (s *Sessions) Destroy(w http.ResponseWriter, r *http.Request, c *Current) {
	if c != nil {
		_ = s.DB.DeleteSession(r.Context(), c.TokenHash)
	}
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(r), Value: "", Path: "/", HttpOnly: true,
		Secure: s.secure(r), SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

// CheckCSRF compare l'en-tête X-CSRF-Token au token de session (temps constant).
func CheckCSRF(r *http.Request, c *Current) bool {
	got := r.Header.Get("X-CSRF-Token")
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(c.CSRFToken)) == 1
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
