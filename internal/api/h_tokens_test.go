package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"rcsui/internal/auth"
	"rcsui/internal/store"
)

// bearer exécute une requête authentifiée par jeton d'API, sans cookie.
func bearer(t *testing.T, h *harness, tok, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.ts.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func newToken(t *testing.T, c *client, body map[string]any) string {
	t.Helper()
	code, out := c.do("POST", "/api/v1/auth/tokens", body)
	if code != 201 {
		t.Fatalf("create token %v: %d %v", body, code, out)
	}
	tok, _ := out["token"].(string)
	if !strings.HasPrefix(tok, auth.APITokenPrefix) || !strings.HasPrefix(tok, out["prefix"].(string)) {
		t.Fatalf("token %q prefix %v", tok, out["prefix"])
	}
	return tok
}

func TestAPITokens(t *testing.T) {
	h := newHarness(t)
	admin := setupAdmin(t, h)
	ctx := context.Background()

	full := newToken(t, admin, map[string]any{"name": "ci"})
	// Le jeton brut n'est jamais relu.
	code, out := admin.do("GET", "/api/v1/auth/tokens", nil)
	if code != 200 || len(items(out)) != 1 || strings.Contains(mustJSON(out), full) || items(out)[0].(map[string]any)["role"] != "admin" {
		t.Fatalf("list: %d %v", code, out)
	}
	if code, _ := admin.do("POST", "/api/v1/auth/tokens", map[string]any{"name": "ci"}); code != 409 {
		t.Errorf("duplicate name: %d", code)
	}

	// Jeton admin : API utilisable sans session ni CSRF.
	if code, _ := bearer(t, h, full, "GET", "/api/v1/jobs", nil); code != 200 {
		t.Fatalf("bearer GET: %d", code)
	}
	if code, out := bearer(t, h, full, "POST", "/api/v1/users", map[string]any{"username": "ops", "password": "ops-password-1234", "role": "operator"}); code != 201 {
		t.Fatalf("bearer POST without CSRF: %d %v", code, out)
	}
	// Mais pas la gestion des jetons ni le mot de passe.
	for _, p := range []string{"/api/v1/auth/tokens", "/api/v1/auth/password"} {
		if code, out := bearer(t, h, full, "POST", p, map[string]any{}); code != 403 || errCode(out) != "session_required" {
			t.Errorf("bearer %s: %d %v", p, code, out)
		}
	}

	// Jeton plafonné à viewer : pas de route admin.
	viewer := newToken(t, admin, map[string]any{"name": "dashboard", "role": "viewer", "expiresInDays": 30})
	if code, _ := bearer(t, h, viewer, "GET", "/api/v1/users", nil); code != 403 {
		t.Errorf("viewer token on admin route: %d", code)
	}
	if code, out := bearer(t, h, viewer, "GET", "/api/v1/auth/me", nil); code != 200 || out["user"].(map[string]any)["role"] != "viewer" {
		t.Errorf("viewer token /me: %d %v", code, out)
	}

	// Un opérateur ne peut pas créer de jeton admin.
	op, opID := addUser(t, h, admin, "operator1", store.RoleOperator)
	if code, out := op.do("POST", "/api/v1/auth/tokens", map[string]any{"name": "x", "role": "admin"}); code != 400 || errCode(out) != "validation" {
		t.Fatalf("escalation: %d %v", code, out)
	}
	opTok := newToken(t, op, map[string]any{"name": "script"})
	if code, _ := bearer(t, h, opTok, "GET", "/api/v1/users", nil); code != 403 {
		t.Errorf("operator token on admin route: %d", code)
	}
	// Jetons privés : un autre utilisateur ne peut pas révoquer celui-ci.
	_, out = op.do("GET", "/api/v1/auth/tokens", nil)
	opTokID := int64(items(out)[0].(map[string]any)["id"].(float64))
	if code, _ := admin.do("DELETE", "/api/v1/auth/tokens/"+itoa(opTokID), nil); code != 404 {
		t.Errorf("revoke someone else's token: %d", code)
	}
	// Compte désactivé : jetons inutilisables.
	tr := true
	if err := h.db.UpdateUser(ctx, opID, store.UserPatch{Disabled: &tr}); err != nil {
		t.Fatal(err)
	}
	if code, _ := bearer(t, h, opTok, "GET", "/api/v1/jobs", nil); code != 401 {
		t.Errorf("disabled user's token: %d", code)
	}

	// Jeton inconnu, autre schéma : 401, sans repli sur le cookie de session.
	if code, _ := bearer(t, h, auth.APITokenPrefix+"nope", "GET", "/api/v1/jobs", nil); code != 401 {
		t.Errorf("unknown token: %d", code)
	}
	req, _ := http.NewRequest("GET", h.ts.URL+"/api/v1/jobs", nil)
	req.Header.Set("Authorization", "Basic YWRtaW46eA==")
	if resp, err := admin.c.Do(req); err != nil || resp.StatusCode != 401 {
		t.Errorf("basic auth with a session cookie: %v %v", resp.StatusCode, err)
	}

	// Jeton expiré.
	raw, hash, prefix := auth.NewAPIToken()
	u, _ := h.db.GetUserByUsername(ctx, "admin")
	past := time.Now().Add(-time.Hour)
	if _, err := h.db.CreateAPIToken(ctx, store.APIToken{UserID: u.ID, Name: "old", TokenHash: hash, Prefix: prefix,
		Role: store.RoleAdmin, CreatedAt: past.Add(-time.Hour), ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}
	if code, _ := bearer(t, h, raw, "GET", "/api/v1/jobs", nil); code != 401 {
		t.Errorf("expired token: %d", code)
	}

	// Révocation.
	_, out = admin.do("GET", "/api/v1/auth/tokens", nil)
	var fullID int64
	for _, it := range items(out) {
		if m := it.(map[string]any); m["name"] == "ci" {
			fullID = int64(m["id"].(float64))
			if m["lastUsedAt"] == nil {
				t.Error("lastUsedAt not recorded")
			}
		}
	}
	if code, _ := admin.do("DELETE", "/api/v1/auth/tokens/"+itoa(fullID), nil); code != 204 {
		t.Fatalf("revoke: %d", code)
	}
	if code, _ := bearer(t, h, full, "GET", "/api/v1/jobs", nil); code != 401 {
		t.Errorf("revoked token: %d", code)
	}
}

func TestAPITokenLimitsAndValidation(t *testing.T) {
	h := newHarness(t)
	admin := setupAdmin(t, h)
	for _, bad := range []map[string]any{
		{"name": ""},
		{"name": strings.Repeat("x", 65)},
		{"name": "a", "role": "root"},
		{"name": "a", "expiresInDays": -1},
		{"name": "a", "expiresInDays": 4000},
	} {
		if code, _ := admin.do("POST", "/api/v1/auth/tokens", bad); code != 400 {
			t.Errorf("accepted %v: %d", bad, code)
		}
	}
	for i := range store.MaxTokensPerUser {
		newToken(t, admin, map[string]any{"name": "t" + itoa(int64(i))})
	}
	if code, out := admin.do("POST", "/api/v1/auth/tokens", map[string]any{"name": "one-more"}); code != 409 || errCode(out) != "too_many_tokens" {
		t.Errorf("limit: %d %v", code, out)
	}
}
