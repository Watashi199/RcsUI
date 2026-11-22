package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"rcsui/internal/store"
)

// --- Gotify -------------------------------------------------------------------

func (n *Notifier) sendGotify(ctx context.Context, c Config, ev Event) error {
	msg := map[string]any{
		"title":    title(ev),
		"message":  message(ev),
		"priority": gotifyPriority(ev.Run.State),
	}
	if u := n.runURL(ev.Run.ID); u != "" {
		msg["extras"] = map[string]any{"client::notification": map[string]any{"click": map[string]string{"url": u}}}
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	h := http.Header{}
	h.Set("X-Gotify-Key", c.GotifyToken) // en-tête plutôt que ?token= : jamais dans les logs d'accès
	return n.postWith(ctx, strings.TrimRight(c.GotifyURL, "/")+"/message", body, h)
}

// gotifyPriority : échelle Gotify 0-10 (8 et plus : notification insistante sur Android).
func gotifyPriority(st store.RunState) int {
	switch st {
	case store.StateFailed, store.StateInterrupted:
		return 8
	case store.StatePartial, store.StateSkipped:
		return 5
	}
	return 2
}

// --- E-mail (SMTP) ------------------------------------------------------------

// Modes de sécurité SMTP.
const (
	SMTPStartTLS = "starttls" // port 587 : STARTTLS obligatoire
	SMTPTLS      = "tls"      // port 465 : TLS implicite
	SMTPNone     = "none"     // relais local sans chiffrement (authentification refusée hors localhost)
)

// SMTPConfig décrit l'envoi d'e-mails.
type SMTPConfig struct {
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Security string   `json:"security"`
	Username string   `json:"username,omitempty"`
	Password string   `json:"password,omitempty"`
	From     string   `json:"from"`
	To       []string `json:"to"`
}

// Validate vérifie la configuration (préfixe d'erreur : nom du champ).
func (s SMTPConfig) Validate() error {
	if s.Host == "" || len(s.Host) > 255 || strings.ContainsAny(s.Host, " \r\n/:@") {
		return errors.New("host: invalid")
	}
	if s.Port < 1 || s.Port > 65535 {
		return errors.New("port: must be between 1 and 65535")
	}
	switch s.Security {
	case SMTPStartTLS, SMTPTLS, SMTPNone:
	default:
		return errors.New("security: must be starttls, tls or none")
	}
	for name, v := range map[string]string{"username": s.Username, "password": s.Password} {
		if len(v) > 512 || strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("%s: invalid", name)
		}
	}
	if s.Password != "" && s.Username == "" {
		return errors.New("username: required with a password")
	}
	if _, err := parseAddr(s.From); err != nil {
		return fmt.Errorf("from: %w", err)
	}
	if len(s.To) == 0 || len(s.To) > 20 {
		return errors.New("to: between 1 and 20 recipients")
	}
	for _, a := range s.To {
		if _, err := parseAddr(a); err != nil {
			return fmt.Errorf("to: %w", err)
		}
	}
	return nil
}

// parseAddr n'accepte qu'une adresse simple (pas de nom affiché, pas de saut de
// ligne) : aucune injection d'en-tête possible.
func parseAddr(a string) (string, error) {
	if a == "" || len(a) > 254 || strings.ContainsAny(a, "\r\n<>,;\"") {
		return "", errors.New("invalid address")
	}
	m, err := mail.ParseAddress(a)
	if err != nil || m.Name != "" || m.Address != a {
		return "", errors.New("invalid address")
	}
	return m.Address, nil
}

const smtpTimeout = 30 * time.Second

func (n *Notifier) sendEmail(ctx context.Context, s SMTPConfig, ev Event) error {
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	deadline := time.Now().Add(smtpTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	dialer := &net.Dialer{Deadline: deadline}
	tlsCfg := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	var conn net.Conn
	var err error
	if s.Security == SMTPTLS {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if err := c.Hello("rcsui"); err != nil {
		return err
	}
	if s.Security == SMTPStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return permanentError{errors.New("server does not offer STARTTLS")}
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return err
		}
	}
	if s.Username != "" {
		// PlainAuth refuse d'envoyer le mot de passe sans TLS, sauf vers localhost.
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return smtpAuthError(err)
		}
	}
	if err := c.Mail(s.From); err != nil {
		return err
	}
	for _, to := range s.To {
		if err := c.Rcpt(to); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(n.emailMessage(s, ev)); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	// Message accepté par le serveur : livré. QUIT n'est qu'un nettoyage, son
	// échec ne doit pas provoquer un renvoi (doublon).
	_ = c.Quit()
	return nil
}

// smtpAuthError classe un échec d'authentification : un refus définitif (5xx,
// ou mot de passe qu'on refuse d'envoyer sans TLS) n'est pas retenté ; un
// refus temporaire (4xx) ou une erreur réseau l'est.
func smtpAuthError(err error) error {
	err = fmt.Errorf("authentication: %w", err)
	var tp *textproto.Error
	if errors.As(err, &tp) {
		if tp.Code >= 500 {
			return permanentError{err}
		}
		return err
	}
	var ne net.Error
	if errors.As(err, &ne) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	return permanentError{err}
}

// emailMessage construit le message (texte brut, UTF-8 en quoted-printable).
func (n *Notifier) emailMessage(s SMTPConfig, ev Event) []byte {
	var body bytes.Buffer
	qp := quotedprintable.NewWriter(&body)
	text := message(ev)
	if u := n.runURL(ev.Run.ID); u != "" {
		text += "\n\n" + u
	}
	_, _ = qp.Write([]byte(strings.ReplaceAll(text, "\n", "\r\n") + "\r\n"))
	_ = qp.Close()

	id := make([]byte, 12)
	_, _ = rand.Read(id)
	domain := s.From[strings.LastIndex(s.From, "@")+1:]
	var b bytes.Buffer
	hdr := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	hdr("From", s.From)
	hdr("To", strings.Join(s.To, ", "))
	hdr("Subject", mime.QEncoding.Encode("utf-8", title(ev)))
	hdr("Date", time.Now().Format(time.RFC1123Z))
	hdr("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	hdr("MIME-Version", "1.0")
	hdr("Content-Type", "text/plain; charset=utf-8")
	hdr("Content-Transfer-Encoding", "quoted-printable")
	hdr("Auto-Submitted", "auto-generated")
	b.WriteString("\r\n")
	b.Write(body.Bytes())
	return b.Bytes()
}
