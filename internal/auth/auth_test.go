package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rcsui/internal/store"
)

func TestPasswordHashVerify(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Errorf("format %q", h)
	}
	if err := VerifyPassword("correct horse battery", h); err != nil {
		t.Errorf("verify: %v", err)
	}
	if err := VerifyPassword("wrong", h); err != ErrMismatch {
		t.Errorf("wrong pw: %v", err)
	}
	if err := VerifyPassword("x", ""); err != ErrMismatch {
		t.Errorf("empty hash: %v", err)
	}
	if err := VerifyPassword("x", "$argon2id$v=19$m=99999999,t=3,p=2$AAAA$AAAA"); err == nil {
		t.Error("huge params accepted")
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Error("salt not random")
	}
	if CheckPasswordPolicy("short") == nil || CheckPasswordPolicy("long enough pw") != nil {
		t.Error("policy")
	}
	if p := RandomPassword(20); len(p) != 20 {
		t.Errorf("random password %q", p)
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter()
	clock := time.Now()
	l.now = func() time.Time { return clock }
	for i := 0; i < 5; i++ {
		if ok, _ := l.Allow("ip:1", "user:a"); !ok {
			t.Fatalf("attempt %d refused", i)
		}
	}
	ok, wait := l.Allow("ip:1", "user:b")
	if ok || wait <= 0 {
		t.Errorf("6th attempt from same ip allowed (wait=%v)", wait)
	}
	if ok, _ := l.Allow("ip:2", "user:a"); ok {
		t.Error("same user from other ip allowed")
	}
	clock = clock.Add(13 * time.Second)
	if ok, _ := l.Allow("ip:1", "user:c"); !ok {
		t.Error("refill failed")
	}
}

func newSessions(t *testing.T) (*Sessions, store.User) {
	dir := t.TempDir()
	db, err := store.Open(context.Background(), filepath.Join(dir, "t.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	id, _ := db.CreateUser(context.Background(), store.User{Username: "u", Role: store.RoleAdmin})
	u, _ := db.GetUser(context.Background(), id)
	return &Sessions{DB: db, Idle: time.Hour, Absolute: 24 * time.Hour, CookieSecure: "auto",
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}, u
}

func TestSessionLifecycle(t *testing.T) {
	s, u := newSessions(t)
	r := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	w := httptest.NewRecorder()
	cur, err := s.Create(w, r, u)
	if err != nil {
		t.Fatal(err)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Name != "rcsui_session" {
		t.Fatalf("cookie %+v", cookies)
	}
	r2 := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	r2.AddCookie(cookies[0])
	got, err := s.Lookup(r2)
	if err != nil || got.User.ID != u.ID || got.CSRFToken != cur.CSRFToken {
		t.Fatalf("lookup %+v %v", got, err)
	}
	r2.Header.Set("X-CSRF-Token", cur.CSRFToken)
	if !CheckCSRF(r2, got) {
		t.Error("csrf ok rejected")
	}
	r2.Header.Set("X-CSRF-Token", "nope")
	if CheckCSRF(r2, got) {
		t.Error("bad csrf accepted")
	}
	s.Destroy(httptest.NewRecorder(), r2, got)
	if _, err := s.Lookup(r2); err == nil {
		t.Error("destroyed session still valid")
	}
	r3 := httptest.NewRequest("GET", "/", nil)
	r3.AddCookie(&http.Cookie{Name: "rcsui_session", Value: "forged"})
	if _, err := s.Lookup(r3); err == nil {
		t.Error("forged cookie accepted")
	}
}

func TestSecureCookieBehindTrustedProxy(t *testing.T) {
	s, u := newSessions(t)
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "10.1.2.3:5555"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	w := httptest.NewRecorder()
	if _, err := s.Create(w, r, u); err != nil {
		t.Fatal(err)
	}
	c := w.Result().Cookies()[0]
	if !c.Secure || c.Name != "__Host-rcsui_session" {
		t.Errorf("cookie %+v", c)
	}
	if ip := s.ClientIP(r); ip != "203.0.113.9" {
		t.Errorf("client ip %q", ip)
	}
	r.RemoteAddr = "192.0.2.1:1"
	if s.IsHTTPS(r) || s.ClientIP(r) != "192.0.2.1" {
		t.Error("untrusted forwarded headers honoured")
	}
}

func TestIdleExpiry(t *testing.T) {
	s, u := newSessions(t)
	s.Idle = time.Millisecond
	w := httptest.NewRecorder()
	s.Create(w, httptest.NewRequest("POST", "/", nil), u)
	time.Sleep(5 * time.Millisecond)
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(w.Result().Cookies()[0])
	if _, err := s.Lookup(r); err == nil {
		t.Error("idle session accepted")
	}
}
