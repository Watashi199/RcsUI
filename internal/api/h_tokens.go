package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rcsui/internal/auth"
	"rcsui/internal/engine"
	"rcsui/internal/store"
)

// Jetons d'API personnels (M11) : gérés uniquement depuis une session
// (tokenForbidden), propres à l'utilisateur courant.

const maxTokenDays = 3650

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	list, err := s.DB.ListAPITokens(r.Context(), cur.User.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

type createdToken struct {
	store.APIToken
	Token string `json:"token"` // montré une seule fois
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	var in struct {
		Name          string     `json:"name"`
		Role          store.Role `json:"role"` // défaut : rôle de l'utilisateur
		ExpiresInDays int        `json:"expiresInDays"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Role == "" {
		in.Role = cur.User.Role
	}
	ve := &engine.ValidationError{}
	if !validJobName(in.Name) || len(in.Name) > 64 {
		ve.Add("name", "required, 1-64 characters, no control characters")
	}
	if !in.Role.Valid() || in.Role.Level() > cur.User.Role.Level() {
		ve.Add("role", "must be a role no higher than yours")
	}
	if in.ExpiresInDays < 0 || in.ExpiresInDays > maxTokenDays {
		ve.Add("expiresInDays", "must be between 0 (never) and 3650")
	}
	if err := ve.Err(); err != nil {
		s.fail(w, r, err)
		return
	}
	raw, hash, prefix := auth.NewAPIToken()
	now := time.Now().UTC()
	t := store.APIToken{UserID: cur.User.ID, Name: in.Name, TokenHash: hash, Prefix: prefix, Role: in.Role, CreatedAt: now}
	if in.ExpiresInDays > 0 {
		exp := now.Add(time.Duration(in.ExpiresInDays) * 24 * time.Hour)
		t.ExpiresAt = &exp
	}
	id, err := s.DB.CreateAPIToken(r.Context(), t)
	if errors.Is(err, store.ErrTooManyTokens) {
		writeError(w, http.StatusConflict, "too_many_tokens", "token limit reached (20); revoke unused tokens first")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	t.ID = id
	s.audit(r, cur, "token.create", in.Name, map[string]any{"role": in.Role, "expiresInDays": in.ExpiresInDays})
	writeJSON(w, http.StatusCreated, createdToken{APIToken: t, Token: raw})
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.DB.DeleteAPIToken(r.Context(), cur.User.ID, id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.audit(r, cur, "token.revoke", strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}
