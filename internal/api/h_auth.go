package api

import (
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"rcsui/internal/auth"
	"rcsui/internal/store"
)

type meResponse struct {
	User        store.User `json:"user"`
	CSRFToken   string     `json:"csrfToken"`
	OIDCEnabled bool       `json:"oidcEnabled"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	name := strings.ToLower(strings.TrimSpace(in.Username))
	ip := s.Sessions.ClientIP(r)
	if ok, wait := s.Limiter.Allow("ip:"+ip, "user:"+name); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many login attempts, retry later")
		return
	}
	u, err := s.DB.GetUserByUsername(r.Context(), name)
	if err != nil {
		auth.DummyVerify(in.Password) // même coût : n'indique pas si le compte existe
		s.audit(r, nil, "auth.login_failed", "user:"+name, map[string]any{"ip": ip})
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	if err := auth.VerifyPassword(in.Password, u.PasswordHash); err != nil || u.Disabled {
		s.audit(r, nil, "auth.login_failed", "user:"+name, map[string]any{"ip": ip})
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	cur, err := s.Sessions.Create(w, r, u)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.Limiter.Reset("user:" + name)
	_ = s.DB.TouchLogin(r.Context(), u.ID)
	s.audit(r, cur, "auth.login", "user:"+u.Username, map[string]any{"ip": ip})
	writeJSON(w, http.StatusOK, meResponse{User: u, CSRFToken: cur.CSRFToken, OIDCEnabled: s.Info.OIDCEnabled})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	s.Sessions.Destroy(w, r, cur)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	writeJSON(w, http.StatusOK, meResponse{User: cur.User, CSRFToken: cur.CSRFToken, OIDCEnabled: s.Info.OIDCEnabled})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if ok, _ := s.Limiter.Allow("pwchange:" + strconv.FormatInt(cur.User.ID, 10)); !ok {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many attempts")
		return
	}
	if err := auth.VerifyPassword(in.Current, cur.User.PasswordHash); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_credentials", "current password is incorrect")
		return
	}
	if err := auth.CheckPasswordPolicy(in.New); err != nil {
		writeError(w, http.StatusBadRequest, "weak_password", err.Error())
		return
	}
	if in.New == in.Current {
		writeError(w, http.StatusBadRequest, "weak_password", "new password must differ from the current one")
		return
	}
	h, err := auth.HashPassword(in.New)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	f := false
	if err := s.DB.UpdateUser(r.Context(), cur.User.ID, store.UserPatch{PasswordHash: &h, MustChangePW: &f}); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.DB.DeleteUserSessions(r.Context(), cur.User.ID, cur.TokenHash)
	s.audit(r, cur, "auth.password_changed", "user:"+cur.User.Username, nil)
	w.WriteHeader(http.StatusNoContent)
}

// --- utilisateurs (admin) -----------------------------------------------------

func validUsername(n string) bool {
	if len(n) < 2 || len(n) > 64 {
		return false
	}
	for _, c := range n {
		if !(unicode.IsLetter(c) || unicode.IsDigit(c) || strings.ContainsRune("._-@", c)) {
			return false
		}
	}
	return true
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request, _ *auth.Current) {
	us, err := s.DB.ListUsers(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": us})
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	var in struct {
		Username string     `json:"username"`
		Password string     `json:"password"`
		Role     store.Role `json:"role"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if !validUsername(in.Username) {
		writeError(w, http.StatusBadRequest, "validation", "invalid username")
		return
	}
	if !in.Role.Valid() {
		writeError(w, http.StatusBadRequest, "validation", "invalid role")
		return
	}
	if err := auth.CheckPasswordPolicy(in.Password); err != nil {
		writeError(w, http.StatusBadRequest, "weak_password", err.Error())
		return
	}
	h, err := auth.HashPassword(in.Password)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	id, err := s.DB.CreateUser(r.Context(), store.User{Username: in.Username, PasswordHash: h, Role: in.Role, MustChangePW: true})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.audit(r, cur, "user.create", "user:"+in.Username, map[string]any{"role": in.Role})
	u, _ := s.DB.GetUser(r.Context(), id)
	writeJSON(w, http.StatusCreated, u)
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, badRequest("invalid id")
	}
	return id, nil
}

func (s *Server) handlePatchUser(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var in struct {
		Role     *store.Role `json:"role"`
		Disabled *bool       `json:"disabled"`
		Password *string     `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	target, err := s.DB.GetUser(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p := store.UserPatch{Role: in.Role, Disabled: in.Disabled}
	if in.Role != nil && !in.Role.Valid() {
		writeError(w, http.StatusBadRequest, "validation", "invalid role")
		return
	}
	demotes := (in.Role != nil && *in.Role != store.RoleAdmin) || (in.Disabled != nil && *in.Disabled)
	if target.Role == store.RoleAdmin && demotes {
		if n, _ := s.DB.CountActiveAdmins(r.Context()); n <= 1 {
			writeError(w, http.StatusConflict, "last_admin", "cannot demote or disable the last administrator")
			return
		}
	}
	if in.Password != nil {
		if err := auth.CheckPasswordPolicy(*in.Password); err != nil {
			writeError(w, http.StatusBadRequest, "weak_password", err.Error())
			return
		}
		h, err := auth.HashPassword(*in.Password)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		t := true
		p.PasswordHash, p.MustChangePW = &h, &t
	}
	if err := s.DB.UpdateUser(r.Context(), id, p); err != nil {
		s.fail(w, r, err)
		return
	}
	// Tout changement de droits ou de mot de passe révoque les sessions de la cible.
	var keep []byte
	if id == cur.User.ID {
		keep = cur.TokenHash
	}
	_ = s.DB.DeleteUserSessions(r.Context(), id, keep)
	s.audit(r, cur, "user.update", "user:"+target.Username, map[string]any{
		"role": in.Role, "disabled": in.Disabled, "passwordReset": in.Password != nil})
	u, _ := s.DB.GetUser(r.Context(), id)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if id == cur.User.ID {
		writeError(w, http.StatusConflict, "self_delete", "you cannot delete your own account")
		return
	}
	target, err := s.DB.GetUser(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if target.Role == store.RoleAdmin {
		if n, _ := s.DB.CountActiveAdmins(r.Context()); n <= 1 {
			writeError(w, http.StatusConflict, "last_admin", "cannot delete the last administrator")
			return
		}
	}
	if err := s.DB.DeleteUser(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.audit(r, cur, "user.delete", "user:"+target.Username, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request, _ *auth.Current) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	l, err := s.DB.ListAudit(r.Context(), before, limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": l})
}

// --- système & réglages ------------------------------------------------------

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request, _ *auth.Current) {
	info := s.Info
	info.MaxConcurrent = s.Runner.MaxConcurrent()
	info.AllowedRoots = s.Paths.Roots()
	if info.AllowedRoots == nil {
		info.AllowedRoots = []string{}
	}
	writeJSON(w, http.StatusOK, info)
}

// Settings sont les réglages modifiables à chaud.
type Settings struct {
	MaxConcurrent          int  `json:"maxConcurrent"`
	RsyncVanishedIsSuccess bool `json:"rsyncVanishedIsSuccess"`
	LogRetentionDays       int  `json:"logRetentionDays"`
}

// Clés de réglages en base.
const (
	SettingMaxConcurrent   = "max_concurrent"
	SettingVanishedSuccess = "rsync_vanished_is_success"
	SettingLogRetention    = "log_retention_days"
)

// LoadSettings lit les réglages avec leurs valeurs par défaut.
func LoadSettings(r *http.Request, db *store.DB, defMax, defRetention int) Settings {
	ctx := r.Context()
	return Settings{
		MaxConcurrent:          db.GetIntSetting(ctx, SettingMaxConcurrent, defMax),
		RsyncVanishedIsSuccess: db.GetBoolSetting(ctx, SettingVanishedSuccess, true),
		LogRetentionDays:       db.GetIntSetting(ctx, SettingLogRetention, defRetention),
	}
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request, _ *auth.Current) {
	st := LoadSettings(r, s.DB, s.Runner.MaxConcurrent(), 90)
	st.MaxConcurrent = s.Runner.MaxConcurrent()
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	var in Settings
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if in.MaxConcurrent < 1 || in.MaxConcurrent > 64 {
		writeError(w, http.StatusBadRequest, "validation", "maxConcurrent must be between 1 and 64")
		return
	}
	if in.LogRetentionDays < 1 || in.LogRetentionDays > 3650 {
		writeError(w, http.StatusBadRequest, "validation", "logRetentionDays must be between 1 and 3650")
		return
	}
	ctx := r.Context()
	for k, v := range map[string]string{
		SettingMaxConcurrent:   strconv.Itoa(in.MaxConcurrent),
		SettingVanishedSuccess: strconv.FormatBool(in.RsyncVanishedIsSuccess),
		SettingLogRetention:    strconv.Itoa(in.LogRetentionDays),
	} {
		if err := s.DB.SetSetting(ctx, k, v); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	s.Runner.SetMaxConcurrent(in.MaxConcurrent)
	s.audit(r, cur, "settings.update", "", map[string]any{"maxConcurrent": in.MaxConcurrent,
		"rsyncVanishedIsSuccess": in.RsyncVanishedIsSuccess, "logRetentionDays": in.LogRetentionDays})
	writeJSON(w, http.StatusOK, in)
}
