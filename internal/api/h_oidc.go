package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"rcsui/internal/auth"
	"rcsui/internal/store"
)

func (s *Server) handleAuthConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"oidcEnabled": s.OIDC != nil})
}

func (s *Server) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	if s.OIDC == nil {
		writeError(w, http.StatusNotFound, "oidc_disabled", "OIDC is not configured")
		return
	}
	s.OIDC.Start(w, r, s.Sessions.IsHTTPS(r) || s.Sessions.CookieSecure == "true")
}

var usernameSanitize = regexp.MustCompile(`[^A-Za-z0-9._@-]`)

// handleOIDCCallback termine le flux. Les erreurs détaillées vont dans les logs
// et l'audit ; l'utilisateur est renvoyé vers /login avec un code générique.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if s.OIDC == nil {
		writeError(w, http.StatusNotFound, "oidc_disabled", "OIDC is not configured")
		return
	}
	fail := func(code string, err error) {
		s.Log.Warn("oidc login failed", "reason", code, "err", err)
		s.audit(r, nil, "auth.oidc_failed", "", map[string]any{"reason": code, "ip": s.Sessions.ClientIP(r)})
		http.Redirect(w, r, "/login?error="+code, http.StatusFound)
	}
	id, err := s.OIDC.Finish(w, r)
	if err != nil {
		fail("sso_failed", err)
		return
	}
	ctx := r.Context()
	role, mapped := s.OIDC.RoleFor(id.Groups)
	u, err := s.DB.GetUserByOIDC(ctx, id.Issuer, id.Subject)
	switch {
	case errors.Is(err, store.ErrNotFound):
		if !s.OIDC.AutoProvision() {
			fail("sso_not_provisioned", fmt.Errorf("no account for %s (auto-provisioning disabled)", id.Subject))
			return
		}
		u, err = s.provisionOIDCUser(r, id, role)
		if err != nil {
			fail("sso_failed", err)
			return
		}
	case err != nil:
		fail("sso_failed", err)
		return
	case u.Disabled:
		fail("sso_disabled", fmt.Errorf("account %s is disabled", u.Username))
		return
	case mapped && u.Role != role:
		// Les rôles suivent les groupes de l'IdP à chaque connexion.
		if u.Role == store.RoleAdmin && role != store.RoleAdmin {
			if n, _ := s.DB.CountActiveAdmins(ctx); n <= 1 {
				fail("sso_last_admin", fmt.Errorf("refusing to demote the last administrator %s", u.Username))
				return
			}
		}
		if err := s.DB.UpdateUser(ctx, u.ID, store.UserPatch{Role: &role}); err != nil {
			fail("sso_failed", err)
			return
		}
		_ = s.DB.DeleteUserSessions(ctx, u.ID, nil)
		s.audit(r, nil, "user.role_from_oidc", "user:"+u.Username, map[string]any{"from": u.Role, "to": role})
		u.Role = role
	}
	cur, err := s.Sessions.Create(w, r, u)
	if err != nil {
		fail("sso_failed", err)
		return
	}
	_ = s.DB.TouchLogin(ctx, u.ID)
	s.audit(r, cur, "auth.login", "user:"+u.Username, map[string]any{"ip": s.Sessions.ClientIP(r), "method": "oidc"})
	http.Redirect(w, r, auth.SafeNext(id.Next), http.StatusFound)
}

// provisionOIDCUser crée un compte OIDC (sans mot de passe local) avec un nom unique.
func (s *Server) provisionOIDCUser(r *http.Request, id auth.Identity, role store.Role) (store.User, error) {
	base := usernameSanitize.ReplaceAllString(id.Username, "_")
	if len(base) < 2 {
		base = "sso_" + usernameSanitize.ReplaceAllString(id.Subject, "_")
	}
	if len(base) > 56 {
		base = base[:56]
	}
	for i := 0; i < 20; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%d", base, i+1)
		}
		uid, err := s.DB.CreateUser(r.Context(), store.User{Username: name, Role: role, OIDCIssuer: id.Issuer, OIDCSubject: id.Subject})
		if errors.Is(err, store.ErrConflict) {
			if _, e := s.DB.GetUserByOIDC(r.Context(), id.Issuer, id.Subject); e == nil {
				return s.DB.GetUserByOIDC(r.Context(), id.Issuer, id.Subject) // course : déjà créé
			}
			continue
		}
		if err != nil {
			return store.User{}, err
		}
		s.audit(r, nil, "user.provisioned", "user:"+name, map[string]any{"issuer": id.Issuer, "role": role})
		return s.DB.GetUser(r.Context(), uid)
	}
	return store.User{}, fmt.Errorf("cannot find a free username for %q", strings.TrimSpace(id.Username))
}
