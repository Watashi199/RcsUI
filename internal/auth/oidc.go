package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"rcsui/internal/store"
)

// OIDCConfig configure la connexion OIDC (PLAN.md §8, risque 12).
type OIDCConfig struct {
	Issuer         string
	ClientID       string
	ClientSecret   string
	RedirectURL    string // BaseURL + /api/v1/auth/oidc/callback
	Scopes         []string
	RoleClaim      string   // ex. "groups"
	UsernameClaim  string   // ex. "preferred_username"
	AdminGroups    []string // membres => admin
	OperatorGroups []string // membres => operator
	AutoProvision  bool     // créer le compte au premier login
}

// OIDC gère le flux authorization code + PKCE.
type OIDC struct {
	cfg      OIDCConfig
	verifier *oidc.IDTokenVerifier
	oauth    oauth2.Config

	mu      sync.Mutex
	pending map[string]pendingLogin
	now     func() time.Time
}

type pendingLogin struct {
	nonce    string
	verifier string
	next     string
	exp      time.Time
}

const (
	oidcCookie     = "rcsui_oidc"
	oidcCookiePath = "/api/v1/auth/oidc"
	oidcPendingTTL = 10 * time.Minute
)

// NewOIDC découvre le fournisseur (/.well-known/openid-configuration).
func NewOIDC(ctx context.Context, cfg OIDCConfig) (*OIDC, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.RedirectURL == "" {
		return nil, errors.New("oidc: issuer, client id and redirect url (RCSUI_BASE_URL) are required")
	}
	p, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}
	if cfg.UsernameClaim == "" {
		cfg.UsernameClaim = "preferred_username"
	}
	return &OIDC{
		cfg:      cfg,
		verifier: p.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		oauth: oauth2.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, Endpoint: p.Endpoint(),
			RedirectURL: cfg.RedirectURL, Scopes: cfg.Scopes},
		pending: map[string]pendingLogin{},
		now:     time.Now,
	}, nil
}

// SafeNext n'accepte qu'un chemin interne (pas de redirection ouverte).
func SafeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

// Start redirige vers le fournisseur ; le state est lié au navigateur par un cookie court.
func (o *OIDC) Start(w http.ResponseWriter, r *http.Request, secure bool) {
	state, nonce, verifier := RandomToken(24), RandomToken(24), oauth2.GenerateVerifier()
	o.mu.Lock()
	t := o.now()
	for k, p := range o.pending {
		if t.After(p.exp) {
			delete(o.pending, k)
		}
	}
	if len(o.pending) > 1000 { // anti-épuisement mémoire
		o.mu.Unlock()
		http.Error(w, "too many pending logins", http.StatusTooManyRequests)
		return
	}
	o.pending[state] = pendingLogin{nonce: nonce, verifier: verifier, next: SafeNext(r.URL.Query().Get("next")), exp: t.Add(oidcPendingTTL)}
	o.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: state, Path: oidcCookiePath, HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: int(oidcPendingTTL.Seconds())})
	http.Redirect(w, r, o.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

// Identity est le résultat d'une authentification OIDC réussie.
type Identity struct {
	Issuer   string
	Subject  string
	Username string
	Groups   []string
	Next     string
}

// ErrOIDC regroupe les échecs de callback (message générique côté utilisateur).
var ErrOIDC = errors.New("oidc authentication failed")

// Finish valide le callback (state, code + PKCE, ID token, nonce) et retourne l'identité.
func (o *OIDC) Finish(w http.ResponseWriter, r *http.Request) (Identity, error) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		return Identity{}, fmt.Errorf("%w: provider returned %s", ErrOIDC, e)
	}
	c, err := r.Cookie(oidcCookie)
	state := q.Get("state")
	if err != nil || state == "" || c.Value != state {
		return Identity{}, fmt.Errorf("%w: state mismatch", ErrOIDC)
	}
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: "", Path: oidcCookiePath, MaxAge: -1, HttpOnly: true})
	o.mu.Lock()
	p, ok := o.pending[state]
	delete(o.pending, state) // usage unique
	o.mu.Unlock()
	if !ok || o.now().After(p.exp) {
		return Identity{}, fmt.Errorf("%w: unknown or expired state", ErrOIDC)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tok, err := o.oauth.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(p.verifier))
	if err != nil {
		return Identity{}, fmt.Errorf("%w: code exchange: %v", ErrOIDC, err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return Identity{}, fmt.Errorf("%w: no id_token", ErrOIDC)
	}
	idt, err := o.verifier.Verify(ctx, raw)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: id_token: %v", ErrOIDC, err)
	}
	if idt.Nonce != p.nonce {
		return Identity{}, fmt.Errorf("%w: nonce mismatch", ErrOIDC)
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		return Identity{}, fmt.Errorf("%w: claims: %v", ErrOIDC, err)
	}
	id := Identity{Issuer: idt.Issuer, Subject: idt.Subject, Next: p.next}
	for _, k := range []string{o.cfg.UsernameClaim, "preferred_username", "email", "sub"} {
		if s, ok := claims[k].(string); ok && s != "" {
			id.Username = s
			break
		}
	}
	id.Groups = stringsClaim(claims[o.cfg.RoleClaim])
	return id, nil
}

func stringsClaim(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, g := range t {
			if s, ok := g.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// RoleFor calcule le rôle depuis les groupes ; ok=false si aucun mapping n'est configuré.
func (o *OIDC) RoleFor(groups []string) (store.Role, bool) {
	if len(o.cfg.AdminGroups) == 0 && len(o.cfg.OperatorGroups) == 0 {
		return store.RoleViewer, false
	}
	in := func(list []string) bool {
		for _, g := range groups {
			for _, want := range list {
				if g == want {
					return true
				}
			}
		}
		return false
	}
	switch {
	case in(o.cfg.AdminGroups):
		return store.RoleAdmin, true
	case in(o.cfg.OperatorGroups):
		return store.RoleOperator, true
	default:
		return store.RoleViewer, true
	}
}

// AutoProvision indique si les comptes inconnus sont créés automatiquement.
func (o *OIDC) AutoProvision() bool { return o.cfg.AutoProvision }
