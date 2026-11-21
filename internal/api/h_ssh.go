package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rcsui/internal/auth"
	"rcsui/internal/authz"
	"rcsui/internal/engine"
	"rcsui/internal/sshkeys"
	"rcsui/internal/store"
)

func (s *Server) requireSSH(w http.ResponseWriter) bool {
	if s.SSH == nil {
		writeError(w, http.StatusServiceUnavailable, "ssh_unavailable", "ssh is not available on this server")
		return false
	}
	return true
}

// canUseIdentity : admin, ou operator ayant reçu l'identité. Le mode « ~/.ssh
// monté » donne accès à toutes les clés de l'hôte : réservé aux admins.
func (s *Server) canUseIdentity(r *http.Request, cur *auth.Current, id engine.SSHIdentity) (bool, error) {
	if authz.IsAdmin(cur.User) {
		return true, nil
	}
	if id.Mounted || id.ManagedID == nil || cur.User.Role != store.RoleOperator || cur.User.Disabled {
		return false, nil
	}
	g, err := s.DB.ListIdentityGrants(r.Context(), cur.User.ID)
	if err != nil {
		return false, err
	}
	return g[*id.ManagedID], nil
}

func (s *Server) handleListIdentities(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	if !s.requireSSH(w) {
		return
	}
	ids, err := s.DB.ListSSHIdentities(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := []store.SSHIdentity{}
	for _, id := range ids {
		mid := id.ID
		if ok, _ := s.canUseIdentity(r, cur, engine.SSHIdentity{ManagedID: &mid}); ok {
			out = append(out, id)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleCreateIdentity(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	if !s.requireSSH(w) {
		return
	}
	var in struct {
		Name       string `json:"name"`
		Generate   bool   `json:"generate"`
		PrivateKey string `json:"privateKey"`
		Passphrase string `json:"passphrase"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	var id store.SSHIdentity
	var err error
	switch {
	case in.Generate && in.PrivateKey == "":
		id, err = s.SSH.Generate(r.Context(), strings.TrimSpace(in.Name))
	case !in.Generate && in.PrivateKey != "":
		id, err = s.SSH.Import(r.Context(), strings.TrimSpace(in.Name), in.PrivateKey, in.Passphrase)
	default:
		err = errors.New("either generate or privateKey is required")
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "conflict", "an identity with this name already exists")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	s.audit(r, cur, "ssh.identity.create", "ssh-identity:"+id.Name, map[string]any{"fingerprint": id.Fingerprint, "generated": in.Generate})
	writeJSON(w, http.StatusCreated, id)
}

func (s *Server) handleDeleteIdentity(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	if !s.requireSSH(w) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.DB.DeleteSSHIdentity(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "identity_in_use", "identity is used by a job")
			return
		}
		s.fail(w, r, err)
		return
	}
	s.audit(r, cur, "ssh.identity.delete", "ssh-identity:"+itoa64(id), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetIdentityGrants(w http.ResponseWriter, r *http.Request, _ *auth.Current) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ids, err := s.DB.ListIdentityGrantUsers(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"userIds": ids})
}

func (s *Server) handlePutIdentityGrants(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var in struct {
		UserIDs []int64 `json:"userIds"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err := s.DB.GetSSHIdentity(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.DB.SetIdentityGrants(r.Context(), id, in.UserIDs); err != nil {
		s.fail(w, r, err)
		return
	}
	s.audit(r, cur, "ssh.identity.grants", "ssh-identity:"+itoa64(id), map[string]any{"userIds": in.UserIDs})
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) handleListKnownHosts(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	if !s.requireSSH(w) {
		return
	}
	if !authz.CanCreateJobs(cur.User) {
		writeError(w, http.StatusForbidden, "forbidden", "not allowed")
		return
	}
	hs, err := s.DB.ListKnownHosts(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": hs})
}

func (s *Server) handleScanHost(w http.ResponseWriter, r *http.Request, _ *auth.Current) {
	if !s.requireSSH(w) {
		return
	}
	var in struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if in.Port == 0 {
		in.Port = 22
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	keys, err := sshkeys.Scan(ctx, strings.TrimSpace(in.Host), in.Port)
	if err != nil {
		writeError(w, http.StatusBadGateway, "scan_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

func (s *Server) handleApproveHost(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	if !s.requireSSH(w) {
		return
	}
	var in struct {
		Host string `json:"host"`
		Port int    `json:"port"`
		Key  string `json:"key"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if in.Port == 0 {
		in.Port = 22
	}
	uid := cur.User.ID
	h, err := s.SSH.Approve(r.Context(), strings.TrimSpace(in.Host), in.Port, in.Key, &uid)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	s.audit(r, cur, "ssh.known_host.approve", "host:"+h.Host, map[string]any{"port": h.Port, "fingerprint": h.Fingerprint})
	writeJSON(w, http.StatusCreated, h)
}

func (s *Server) handleDeleteKnownHost(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	if !s.requireSSH(w) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.SSH.Remove(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.audit(r, cur, "ssh.known_host.delete", "known-host:"+itoa64(id), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestSSH(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	if !s.requireSSH(w) {
		return
	}
	var t engine.SSHTarget
	if err := decode(r, &t); err != nil {
		s.fail(w, r, err)
		return
	}
	if t.Port == 0 {
		t.Port = 22
	}
	ok, err := s.canUseIdentity(r, cur, t.Identity)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !ok {
		writeError(w, http.StatusForbidden, "forbidden", "you are not allowed to use this identity")
		return
	}
	out, err := s.SSH.TestConnection(r.Context(), &t)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "output": out})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out})
}

func itoa64(v int64) string { return strconv.FormatInt(v, 10) }
