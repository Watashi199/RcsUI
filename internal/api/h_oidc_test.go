package api

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"rcsui/internal/auth"
	"rcsui/internal/store"
)

// fakeIdP est un fournisseur OIDC minimal (découverte, JWKS, token avec PKCE S256).
type fakeIdP struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	mu     sync.Mutex
	codes  map[string]codeInfo
	sub    string
	groups []string
	// badNonce : l'IdP renvoie un nonce falsifié.
	badNonce bool
}

type codeInfo struct{ nonce, challenge string }

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func newFakeIdP(t *testing.T) *fakeIdP {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	f := &fakeIdP{key: k, codes: map[string]codeInfo{}, sub: "user-123"}
	mux := http.NewServeMux()
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize", "token_endpoint": f.srv.URL + "/token",
			"jwks_uri": f.srv.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
			"n": b64(k.N.Bytes()), "e": b64(big.NewInt(int64(k.E)).Bytes())}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		ci, ok := f.codes[r.Form.Get("code")]
		delete(f.codes, r.Form.Get("code"))
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !ok || b64(sum[:]) != ci.challenge {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		nonce := ci.nonce
		if f.badNonce {
			nonce = "forged"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 60,
			"id_token": f.sign(map[string]any{"iss": f.srv.URL, "aud": "rcsui", "sub": f.sub, "nonce": nonce,
				"iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix(),
				"preferred_username": "alice", "groups": f.groups})})
	})
	return f
}

func (f *fakeIdP) sign(claims map[string]any) string {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	p, _ := json.Marshal(claims)
	in := b64(h) + "." + b64(p)
	sum := sha256.Sum256([]byte(in))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	return in + "." + b64(sig)
}

// authorize simule l'IdP : récupère state/nonce/challenge depuis l'URL de redirection.
func (f *fakeIdP) authorize(t *testing.T, loc string) (state, code string) {
	u, err := url.Parse(loc)
	if err != nil || !strings.HasPrefix(loc, f.srv.URL+"/authorize") {
		t.Fatalf("unexpected redirect %q", loc)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" {
		t.Fatalf("PKCE/nonce missing: %v", q)
	}
	code = auth.RandomToken(8)
	f.mu.Lock()
	f.codes[code] = codeInfo{nonce: q.Get("nonce"), challenge: q.Get("code_challenge")}
	f.mu.Unlock()
	return q.Get("state"), code
}

func withOIDC(t *testing.T, h *harness, idp *fakeIdP, autoProvision bool) {
	t.Helper()
	o, err := auth.NewOIDC(context.Background(), auth.OIDCConfig{Issuer: idp.srv.URL, ClientID: "rcsui", ClientSecret: "s",
		RedirectURL: "http://rcsui.test/api/v1/auth/oidc/callback", RoleClaim: "groups",
		AdminGroups: []string{"admins"}, OperatorGroups: []string{"ops"}, AutoProvision: autoProvision})
	if err != nil {
		t.Fatal(err)
	}
	h.server.OIDC = o
	h.ts.Close()
	h.ts = httptest.NewServer(h.server.Handler())
	t.Cleanup(h.ts.Close)
}

// ssoLogin déroule le flux complet ; retourne la redirection finale.
func ssoLogin(t *testing.T, h *harness, idp *fakeIdP, c *http.Client, tamperState bool) string {
	t.Helper()
	resp, err := c.Get(h.ts.URL + "/api/v1/auth/oidc/login?next=/jobs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	state, code := idp.authorize(t, resp.Header.Get("Location"))
	if tamperState {
		state = "evil"
	}
	resp, err = c.Get(h.ts.URL + "/api/v1/auth/oidc/callback?state=" + url.QueryEscape(state) + "&code=" + code)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.Header.Get("Location")
}

func noRedirectClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestOIDCFlow(t *testing.T) {
	idp := newFakeIdP(t)
	h := newHarness(t)
	withOIDC(t, h, idp, false)
	ctx := context.Background()

	c := noRedirectClient()
	if r, _ := c.Get(h.ts.URL + "/api/v1/auth/config"); r == nil || r.StatusCode != 200 {
		t.Fatal("auth config")
	}
	if loc := ssoLogin(t, h, idp, c, false); loc != "/login?error=sso_not_provisioned" {
		t.Fatalf("unprovisioned: %q", loc)
	}

	withOIDC(t, h, idp, true)
	idp.groups = []string{"ops"}
	c = noRedirectClient()
	if loc := ssoLogin(t, h, idp, c, false); loc != "/jobs" {
		t.Fatalf("provision login: %q", loc)
	}
	u, err := h.db.GetUserByOIDC(ctx, idp.srv.URL, "user-123")
	if err != nil || u.Role != store.RoleOperator || u.Username != "alice" || u.PasswordHash != "" {
		t.Fatalf("provisioned user %+v %v", u, err)
	}
	resp, _ := c.Get(h.ts.URL + "/api/v1/auth/me")
	if resp.StatusCode != 200 {
		t.Errorf("session after sso: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Login suivant : le rôle suit les groupes de l'IdP.
	idp.groups = []string{"admins"}
	c2 := noRedirectClient()
	ssoLogin(t, h, idp, c2, false)
	u, _ = h.db.GetUserByOIDC(ctx, idp.srv.URL, "user-123")
	if u.Role != store.RoleAdmin {
		t.Errorf("role not updated: %s", u.Role)
	}
	// Compte OIDC : le login par mot de passe est impossible.
	lc := h.client(t)
	if code := lc.login("alice", ""); code == 200 {
		t.Error("password login on OIDC account")
	}

	// state falsifié, nonce falsifié : refusés.
	if loc := ssoLogin(t, h, idp, noRedirectClient(), true); loc != "/login?error=sso_failed" {
		t.Errorf("tampered state: %q", loc)
	}
	idp.badNonce = true
	if loc := ssoLogin(t, h, idp, noRedirectClient(), false); loc != "/login?error=sso_failed" {
		t.Errorf("forged nonce: %q", loc)
	}
	idp.badNonce = false

	// Redirection ouverte refusée.
	for in, want := range map[string]string{"//evil.example": "/", "https://evil.example": "/", "/runs/1": "/runs/1", "/\\evil": "/"} {
		if got := auth.SafeNext(in); got != want {
			t.Errorf("SafeNext(%q)=%q", in, got)
		}
	}
}
