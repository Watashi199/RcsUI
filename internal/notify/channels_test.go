package notify

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"rcsui/internal/store"
)

func TestGotify(t *testing.T) {
	var mu sync.Mutex
	var got struct {
		path, key string
		body      map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		got.path, got.key = r.URL.Path, r.Header.Get("X-Gotify-Key")
		_ = json.Unmarshal(b, &got.body)
	}))
	defer srv.Close()
	n := newNotifier(t)
	c := Config{On: ModeAll, GotifyURL: srv.URL + "/gotify/", GotifyToken: "app-token"}
	ev := Event{Run: store.Run{ID: "R1", JobName: "photos", State: store.StateFailed, Engine: "rsync"}}
	if err := n.sendGotify(context.Background(), c, ev); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got.path != "/gotify/message" || got.key != "app-token" || got.body["priority"] != float64(8) ||
		got.body["title"] != "RcsUI: photos failed" {
		t.Fatalf("gotify request: %+v", got)
	}
	extras, _ := json.Marshal(got.body["extras"])
	if !strings.Contains(string(extras), "https://rcsui.example/runs/R1") {
		t.Errorf("click url missing: %s", extras)
	}
	if err := (Config{On: ModeAll, GotifyURL: srv.URL}).Validate(); err == nil {
		t.Error("gotify URL without token accepted")
	}
}

// fakeSMTP est un serveur SMTP minimal (sans TLS) qui enregistre les messages.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	auth string
	from string
	to   []string
	data string
}

func startSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	say := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			say("250-fake")
			say("250 AUTH PLAIN")
		case strings.HasPrefix(cmd, "AUTH PLAIN"):
			b, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(line[len("AUTH PLAIN"):]))
			f.mu.Lock()
			f.auth = string(b)
			f.mu.Unlock()
			if string(b) == "\x00user\x00secret" {
				say("235 ok")
			} else {
				say("535 bad credentials")
			}
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			f.mu.Lock()
			f.from = line[len("MAIL FROM:"):]
			f.mu.Unlock()
			say("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			f.mu.Lock()
			f.to = append(f.to, line[len("RCPT TO:"):])
			f.mu.Unlock()
			say("250 ok")
		case cmd == "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.data = b.String()
			f.mu.Unlock()
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("502 unknown")
		}
	}
}

func TestEmail(t *testing.T) {
	f := startSMTP(t)
	n := newNotifier(t)
	s := SMTPConfig{Host: "127.0.0.1", Port: f.port(), Security: SMTPNone, Username: "user", Password: "secret",
		From: "rcsui@example.org", To: []string{"ops@example.org", "me@example.org"}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	ev := Event{Run: store.Run{ID: "R7", JobName: "photos → nas", Engine: "rclone", State: store.StatePartial,
		ErrorMessage: "2 files failed"}}
	if err := n.sendEmail(context.Background(), s, ev); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	auth, from, to, data := f.auth, f.from, f.to, f.data
	f.mu.Unlock()
	if auth != "\x00user\x00secret" || from != "<rcsui@example.org>" || len(to) != 2 {
		t.Fatalf("envelope: auth=%q from=%q to=%v", auth, from, to)
	}
	head, body, _ := strings.Cut(data, "\r\n\r\n")
	var subject string
	for _, h := range strings.Split(head, "\r\n") {
		if v, ok := strings.CutPrefix(h, "Subject: "); ok {
			subject, _ = new(mime.WordDecoder).DecodeHeader(v)
		}
	}
	if subject != "RcsUI: photos → nas partial" || !strings.Contains(head, "To: ops@example.org, me@example.org") {
		t.Errorf("headers:\n%s", head)
	}
	plain, _ := io.ReadAll(quotedprintable.NewReader(strings.NewReader(body)))
	if !strings.Contains(string(plain), "2 files failed") || !strings.Contains(string(plain), "https://rcsui.example/runs/R7") {
		t.Errorf("body: %s", plain)
	}

	// Mauvais identifiants : erreur définitive (pas de nouvelle tentative).
	s.Password = "wrong"
	err := n.sendEmail(context.Background(), s, ev)
	var p permanentError
	if err == nil || !errors.As(err, &p) {
		t.Errorf("bad credentials: %v", err)
	}
	// STARTTLS exigé mais non proposé : refus.
	s.Security, s.Password = SMTPStartTLS, "secret"
	if err := n.sendEmail(context.Background(), s, ev); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("starttls: %v", err)
	}
}

func TestEmailDeliveredByWorker(t *testing.T) {
	f := startSMTP(t)
	n := newNotifier(t)
	if err := n.Save(context.Background(), Config{On: ModeFailures, SMTP: &SMTPConfig{Host: "127.0.0.1", Port: f.port(),
		Security: SMTPNone, From: "a@example.org", To: []string{"b@example.org"}}}); err != nil {
		t.Fatal(err)
	}
	n.RunFinished(store.Run{ID: "R1", JobName: "j", State: store.StateFailed}, nil)
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.mu.Lock()
		done := f.data != ""
		f.mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no e-mail delivered")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSMTPValidate(t *testing.T) {
	ok := SMTPConfig{Host: "smtp.example.org", Port: 587, Security: SMTPStartTLS, Username: "u", Password: "p",
		From: "a@example.org", To: []string{"b@example.org"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*SMTPConfig){
		"host":          func(s *SMTPConfig) { s.Host = "smtp.example.org:25" },
		"port":          func(s *SMTPConfig) { s.Port = 0 },
		"security":      func(s *SMTPConfig) { s.Security = "ssl" },
		"from name":     func(s *SMTPConfig) { s.From = "RcsUI <a@example.org>" },
		"from inject":   func(s *SMTPConfig) { s.From = "a@example.org\r\nBcc: x@evil.test" },
		"to empty":      func(s *SMTPConfig) { s.To = nil },
		"to list":       func(s *SMTPConfig) { s.To = []string{"a@example.org, c@example.org"} },
		"password only": func(s *SMTPConfig) { s.Username = "" },
		"user newline":  func(s *SMTPConfig) { s.Username = "u\n" },
	} {
		s := ok
		s.To = append([]string(nil), ok.To...)
		mut(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted %+v", name, s)
		}
	}
	if err := (Config{On: ModeAll, SMTP: &SMTPConfig{}}).Validate(); err == nil || !strings.HasPrefix(err.Error(), "smtp.") {
		t.Errorf("smtp errors must be prefixed: %v", err)
	}
}
