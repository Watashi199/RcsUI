package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"rcsui/internal/auth"
	"rcsui/internal/notify"
)

// notifyOut est la configuration renvoyée par l'API : les URL sont masquées
// (elles peuvent contenir un secret) et le jeton ntfy n'est jamais renvoyé.
type notifyOut struct {
	On             notify.Mode `json:"on"`
	IncludeDryRuns bool        `json:"includeDryRuns"`
	WebhookURL     string      `json:"webhookUrl"` // masquée
	WebhookSet     bool        `json:"webhookSet"`
	NtfyURL        string      `json:"ntfyUrl"` // masquée
	NtfySet        bool        `json:"ntfySet"`
	NtfyTokenSet   bool        `json:"ntfyTokenSet"`
}

// notifyIn : un champ secret absent (null) conserve la valeur enregistrée,
// une chaîne vide l'efface.
type notifyIn struct {
	On             notify.Mode `json:"on"`
	IncludeDryRuns bool        `json:"includeDryRuns"`
	WebhookURL     *string     `json:"webhookUrl"`
	NtfyURL        *string     `json:"ntfyUrl"`
	NtfyToken      *string     `json:"ntfyToken"`
}

func toNotifyOut(c notify.Config) notifyOut {
	return notifyOut{On: c.On, IncludeDryRuns: c.IncludeDryRuns,
		WebhookURL: notify.Redact(c.WebhookURL), WebhookSet: c.WebhookURL != "",
		NtfyURL: notify.Redact(c.NtfyURL), NtfySet: c.NtfyURL != "", NtfyTokenSet: c.NtfyToken != ""}
}

// merge applique l'entrée sur la configuration courante.
func (in notifyIn) merge(cur notify.Config) notify.Config {
	c := cur
	c.On, c.IncludeDryRuns = in.On, in.IncludeDryRuns
	if in.WebhookURL != nil {
		c.WebhookURL = *in.WebhookURL
	}
	if in.NtfyURL != nil {
		c.NtfyURL = *in.NtfyURL
	}
	if in.NtfyToken != nil {
		c.NtfyToken = *in.NtfyToken
	}
	return c
}

func (s *Server) notifierOr404(w http.ResponseWriter) bool {
	if s.Notifier == nil {
		writeError(w, http.StatusNotFound, "not_found", "notifications are not available")
		return false
	}
	return true
}

func (s *Server) handleGetNotifications(w http.ResponseWriter, r *http.Request, _ *auth.Current) {
	if !s.notifierOr404(w) {
		return
	}
	writeJSON(w, http.StatusOK, toNotifyOut(s.Notifier.Config()))
}

func (s *Server) handlePutNotifications(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	if !s.notifierOr404(w) {
		return
	}
	var in notifyIn
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	c := in.merge(s.Notifier.Config())
	if err := c.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	if err := s.Notifier.Save(r.Context(), c); err != nil {
		s.fail(w, r, err)
		return
	}
	// Jamais d'URL ni de jeton dans l'audit.
	s.audit(r, cur, "settings.notifications", "", map[string]any{"on": c.On, "includeDryRuns": c.IncludeDryRuns,
		"webhook": c.WebhookURL != "", "ntfy": c.NtfyURL != ""})
	writeJSON(w, http.StatusOK, toNotifyOut(c))
}

// handleTestNotifications envoie un message de test avec la configuration
// courante, éventuellement modifiée par le corps (même format que PUT) sans
// l'enregistrer.
func (s *Server) handleTestNotifications(w http.ResponseWriter, r *http.Request, cur *auth.Current) {
	if !s.notifierOr404(w) {
		return
	}
	var in notifyIn
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if in.On == "" {
		in.On = notify.ModeAll
	}
	c := in.merge(s.Notifier.Config())
	if err := c.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	s.audit(r, cur, "settings.notifications.test", "", nil)
	if err := s.Notifier.Test(ctx, c); err != nil {
		var msg = err.Error()
		if errors.Is(err, context.DeadlineExceeded) {
			msg = "timed out"
		}
		writeError(w, http.StatusBadGateway, "notification_failed", msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
