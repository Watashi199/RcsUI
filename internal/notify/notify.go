// Package notify envoie une notification à la fin des runs (jalon M7) : webhook
// JSON générique et ntfy. La configuration vit dans la table settings, chiffrée
// avec la clé maître (les URL de webhook et les jetons ntfy sont des secrets).
// Les envois sont asynchrones : une file bornée et un worker, avec quelques
// tentatives sur erreur réseau, 429 ou 5xx.
package notify

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rcsui/internal/secrets"
	"rcsui/internal/store"
)

// SettingKey est la clé de la configuration dans la table settings.
const SettingKey = "notifications"

const sealAD = "settings:notifications"

// Mode choisit les runs notifiés.
type Mode string

const (
	ModeNever    Mode = "never"
	ModeFailures Mode = "failures" // failed, partial, interrupted, skipped
	ModeAll      Mode = "all"
)

// Config est la configuration des notifications.
type Config struct {
	On             Mode   `json:"on"`
	IncludeDryRuns bool   `json:"includeDryRuns"`
	WebhookURL     string `json:"webhookUrl,omitempty"`
	NtfyURL        string `json:"ntfyUrl,omitempty"` // URL complète du topic, ex. https://ntfy.sh/mon-topic
	NtfyToken      string `json:"ntfyToken,omitempty"`
}

// Validate vérifie la configuration.
func (c Config) Validate() error {
	switch c.On {
	case ModeNever, ModeFailures, ModeAll:
	default:
		return errors.New("on must be one of never, failures, all")
	}
	if c.WebhookURL != "" {
		if err := checkURL(c.WebhookURL); err != nil {
			return fmt.Errorf("webhookUrl: %w", err)
		}
	}
	if c.NtfyURL != "" {
		if err := checkURL(c.NtfyURL); err != nil {
			return fmt.Errorf("ntfyUrl: %w", err)
		}
		if _, _, err := splitNtfy(c.NtfyURL); err != nil {
			return fmt.Errorf("ntfyUrl: %w", err)
		}
	}
	if len(c.NtfyToken) > 512 || strings.ContainsAny(c.NtfyToken, "\r\n") {
		return errors.New("ntfyToken: invalid")
	}
	return nil
}

// Wants indique si un run dans cet état doit être notifié.
func (c Config) Wants(st store.RunState, dryRun bool) bool {
	if dryRun && !c.IncludeDryRuns {
		return false
	}
	switch c.On {
	case ModeAll:
		return st.Terminal()
	case ModeFailures:
		return isProblem(st)
	}
	return false
}

func isProblem(st store.RunState) bool {
	switch st {
	case store.StateFailed, store.StatePartial, store.StateInterrupted, store.StateSkipped:
		return true
	}
	return false
}

func checkURL(raw string) error {
	if len(raw) > 2048 {
		return errors.New("too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("scheme must be http or https")
	}
	if u.Host == "" {
		return errors.New("host is required")
	}
	return nil
}

// splitNtfy sépare https://host/prefix/topic en (https://host/prefix, topic).
func splitNtfy(raw string) (base, topic string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", err
	}
	p := strings.TrimRight(u.Path, "/")
	i := strings.LastIndex(p, "/")
	if i < 0 || i == len(p)-1 {
		return "", "", errors.New("must end with the topic name, e.g. https://ntfy.sh/my-topic")
	}
	topic = p[i+1:]
	u.Path, u.RawQuery, u.Fragment = p[:i], "", ""
	return strings.TrimRight(u.String(), "/"), topic, nil
}

// Redact masque une URL (secret possible dans le chemin ou la query) : seuls
// le schéma et l'hôte restent visibles.
func Redact(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "…"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// Event est le contenu d'une notification.
type Event struct {
	Run     store.Run
	RetryAt *time.Time
	Test    bool
}

// Notifier envoie les notifications.
type Notifier struct {
	DB      *store.DB
	Box     *secrets.Box
	BaseURL string // pour les liens vers l'UI (peut être vide)
	Client  *http.Client
	Log     *slog.Logger
	// Backoff entre deux tentatives (défaut : 2 s, puis 4 s).
	Backoff time.Duration

	cfg   atomic.Pointer[Config]
	queue chan Event
	wg    sync.WaitGroup
	once  sync.Once
}

// Load lit la configuration en base et démarre le worker.
func (n *Notifier) Load(ctx context.Context) error {
	if n.Client == nil {
		n.Client = &http.Client{Timeout: 15 * time.Second}
	}
	if n.Log == nil {
		n.Log = slog.Default()
	}
	if n.Backoff <= 0 {
		n.Backoff = 2 * time.Second
	}
	cfg := Config{On: ModeNever}
	raw, ok, err := n.DB.GetSetting(ctx, SettingKey)
	if err != nil {
		return err
	}
	if ok {
		sealed, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return fmt.Errorf("notifications setting: %w", err)
		}
		plain, err := n.Box.Open(sealed, []byte(sealAD))
		if err != nil {
			return fmt.Errorf("notifications setting: %w", err)
		}
		if err := json.Unmarshal(plain, &cfg); err != nil {
			return fmt.Errorf("notifications setting: %w", err)
		}
	}
	n.cfg.Store(&cfg)
	n.once.Do(func() {
		n.queue = make(chan Event, 100)
		n.wg.Add(1)
		go n.worker()
	})
	return nil
}

// Config retourne la configuration courante.
func (n *Notifier) Config() Config {
	if c := n.cfg.Load(); c != nil {
		return *c
	}
	return Config{On: ModeNever}
}

// Save valide, chiffre et enregistre la configuration, puis l'applique.
func (n *Notifier) Save(ctx context.Context, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	plain, err := json.Marshal(c)
	if err != nil {
		return err
	}
	sealed, err := n.Box.Seal(plain, []byte(sealAD))
	if err != nil {
		return err
	}
	if err := n.DB.SetSetting(ctx, SettingKey, base64.StdEncoding.EncodeToString(sealed)); err != nil {
		return err
	}
	n.cfg.Store(&c)
	return nil
}

// RunFinished met en file une notification si la configuration le demande.
// Non bloquant : appelé depuis le runner.
func (n *Notifier) RunFinished(r store.Run, retryAt *time.Time) {
	c := n.Config()
	if n.queue == nil || !c.Wants(r.State, r.DryRun) || (c.WebhookURL == "" && c.NtfyURL == "") {
		return
	}
	select {
	case n.queue <- Event{Run: r, RetryAt: retryAt}:
	default:
		n.Log.Warn("notification queue full, dropping", "run", r.ID)
	}
}

// Test envoie un message de test de façon synchrone sur chaque canal configuré.
func (n *Notifier) Test(ctx context.Context, c Config) error {
	if c.WebhookURL == "" && c.NtfyURL == "" {
		return errors.New("no notification channel configured")
	}
	now := time.Now()
	ev := Event{Test: true, Run: store.Run{ID: "TEST", JobName: "RcsUI test notification", Engine: "rsync",
		Trigger: store.TriggerManual, State: store.StateSucceeded, Attempt: 1, QueuedAt: now, FinishedAt: &now}}
	var errs []error
	if c.WebhookURL != "" {
		if err := n.sendWebhook(ctx, c, ev); err != nil {
			errs = append(errs, fmt.Errorf("webhook: %w", err))
		}
	}
	if c.NtfyURL != "" {
		if err := n.sendNtfy(ctx, c, ev); err != nil {
			errs = append(errs, fmt.Errorf("ntfy: %w", err))
		}
	}
	return errors.Join(errs...)
}

// Stop vide la file (borne : ctx) et arrête le worker. À appeler après l'arrêt du runner.
func (n *Notifier) Stop(ctx context.Context) {
	if n.queue == nil {
		return
	}
	close(n.queue)
	done := make(chan struct{})
	go func() { n.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (n *Notifier) worker() {
	defer n.wg.Done()
	for ev := range n.queue {
		c := n.Config()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		if c.WebhookURL != "" {
			if err := n.withRetry(ctx, func() error { return n.sendWebhook(ctx, c, ev) }); err != nil {
				n.Log.Warn("webhook notification failed", "run", ev.Run.ID, "err", err)
			}
		}
		if c.NtfyURL != "" {
			if err := n.withRetry(ctx, func() error { return n.sendNtfy(ctx, c, ev) }); err != nil {
				n.Log.Warn("ntfy notification failed", "run", ev.Run.ID, "err", err)
			}
		}
		cancel()
	}
}

// permanentError : inutile de réessayer (4xx hors 429, requête invalide).
type permanentError struct{ error }

func (n *Notifier) withRetry(ctx context.Context, f func() error) error {
	var err error
	for i := 0; i < 3; i++ {
		if err = f(); err == nil {
			return nil
		}
		var p permanentError
		if errors.As(err, &p) || i == 2 {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(n.Backoff << i):
		}
	}
	return err
}

func (n *Notifier) runURL(id string) string {
	if n.BaseURL == "" || id == "TEST" {
		return ""
	}
	return n.BaseURL + "/runs/" + id
}

// WebhookPayload est le corps JSON envoyé au webhook.
type WebhookPayload struct {
	Event   string     `json:"event"` // "run.finished" ou "test"
	Run     store.Run  `json:"run"`
	RetryAt *time.Time `json:"retryAt,omitempty"`
	URL     string     `json:"url,omitempty"`
	Title   string     `json:"title"`
	Message string     `json:"message"`
}

func (n *Notifier) sendWebhook(ctx context.Context, c Config, ev Event) error {
	p := WebhookPayload{Event: "run.finished", Run: ev.Run, RetryAt: ev.RetryAt, URL: n.runURL(ev.Run.ID),
		Title: title(ev), Message: message(ev)}
	if ev.Test {
		p.Event = "test"
	}
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return n.post(ctx, c.WebhookURL, body, "")
}

func (n *Notifier) sendNtfy(ctx context.Context, c Config, ev Event) error {
	base, topic, err := splitNtfy(c.NtfyURL)
	if err != nil {
		return permanentError{err}
	}
	// Publication JSON (et non par en-têtes) : titres et messages UTF-8 sans encodage particulier.
	msg := map[string]any{
		"topic":    topic,
		"title":    title(ev),
		"message":  message(ev),
		"priority": priority(ev.Run.State),
		"tags":     []string{tag(ev.Run.State)},
	}
	if u := n.runURL(ev.Run.ID); u != "" {
		msg["click"] = u
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return n.post(ctx, base, body, c.NtfyToken)
}

func (n *Notifier) post(ctx context.Context, target string, body []byte, bearer string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return permanentError{err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "RcsUI")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := n.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	err = fmt.Errorf("HTTP %d", resp.StatusCode)
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
		return permanentError{err}
	}
	return err
}

func title(ev Event) string {
	if ev.Test {
		return "RcsUI: test notification"
	}
	t := fmt.Sprintf("RcsUI: %s %s", ev.Run.JobName, ev.Run.State)
	if ev.Run.DryRun {
		t += " (dry-run)"
	}
	return t
}

func message(ev Event) string {
	if ev.Test {
		return "Notifications are working."
	}
	r := ev.Run
	var b strings.Builder
	fmt.Fprintf(&b, "Run %s (%s, %s", r.ID, r.Engine, r.Trigger)
	if r.Attempt > 1 {
		fmt.Fprintf(&b, ", attempt %d", r.Attempt)
	}
	fmt.Fprintf(&b, ") finished: %s.", r.State)
	if r.StartedAt != nil && r.FinishedAt != nil {
		fmt.Fprintf(&b, "\nDuration: %s.", r.FinishedAt.Sub(*r.StartedAt).Round(time.Second))
	}
	if r.FilesDone > 0 || r.BytesDone > 0 {
		fmt.Fprintf(&b, "\nTransferred: %d files, %s.", r.FilesDone, humanBytes(r.BytesDone))
	}
	if r.ErrorsCount > 0 {
		fmt.Fprintf(&b, "\nErrors: %d.", r.ErrorsCount)
	}
	if r.ErrorMessage != "" {
		fmt.Fprintf(&b, "\nError: %s", r.ErrorMessage)
	}
	if ev.RetryAt != nil {
		fmt.Fprintf(&b, "\nAutomatic retry at %s.", ev.RetryAt.Format("2006-01-02 15:04:05 MST"))
	}
	return b.String()
}

func priority(st store.RunState) int {
	switch st {
	case store.StateFailed, store.StateInterrupted:
		return 4
	case store.StatePartial, store.StateSkipped:
		return 3
	}
	return 2
}

func tag(st store.RunState) string {
	switch st {
	case store.StateSucceeded:
		return "white_check_mark"
	case store.StateFailed, store.StateInterrupted:
		return "x"
	}
	return "warning"
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
