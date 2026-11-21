package rclone

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/fs/accounting"

	"rcsui/internal/engine"
)

// logRouter associe un groupe de stats ("run/<id>") au Sink du run.
type logRouter struct {
	mu    sync.RWMutex
	sinks map[string]engine.Sink
}

func newLogRouter() *logRouter { return &logRouter{sinks: map[string]engine.Sink{}} }

func (r *logRouter) register(group string, s engine.Sink) {
	r.mu.Lock()
	r.sinks[group] = s
	r.mu.Unlock()
}

func (r *logRouter) unregister(group string) {
	r.mu.Lock()
	delete(r.sinks, group)
	r.mu.Unlock()
}

func (r *logRouter) get(group string) engine.Sink {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sinks[group]
}

// logHandler est installé via fs.SetLogger. Les enregistrements portant un
// groupe de stats connu vont dans le log du run ; les autres suivent le
// handler rclone par défaut (log système).
type logHandler struct {
	inner  slog.Handler
	router *logRouter
	attrs  []slog.Attr
}

func (h *logHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.inner.Enabled(ctx, l) }

func (h *logHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return &logHandler{inner: h.inner.WithAttrs(a), router: h.router, attrs: append(append([]slog.Attr{}, h.attrs...), a...)}
}

func (h *logHandler) WithGroup(name string) slog.Handler {
	return &logHandler{inner: h.inner.WithGroup(name), router: h.router, attrs: h.attrs}
}

func (h *logHandler) Handle(ctx context.Context, r slog.Record) error {
	group, ok := accounting.StatsGroupFromContext(ctx)
	if !ok {
		return h.inner.Handle(ctx, r)
	}
	sink := h.router.get(group)
	if sink == nil {
		return h.inner.Handle(ctx, r)
	}
	object := ""
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "object" {
			object = a.Value.String()
		}
		return true
	})
	msg := Redact(r.Message)
	full := msg
	if object != "" {
		full = object + ": " + msg
	}
	lvl := engine.LevelInfo
	switch {
	case r.Level >= slog.LevelError:
		lvl = engine.LevelError
	case r.Level >= slog.LevelWarn:
		lvl = engine.LevelWarn
	case r.Level < slog.LevelInfo:
		lvl = engine.LevelDebug
	}
	t := r.Time
	if t.IsZero() {
		t = time.Now()
	}
	sink.OnLog(engine.LogLine{Time: t, Stream: "rclone", Level: lvl, Msg: full})
	if it, ok := ItemFromLog(object, msg); ok {
		it.Time = t
		sink.OnItem(it)
	}
	return nil
}

var sizeRe = regexp.MustCompile(`\(size ([0-9.]+)([KMGTP]i)?\)`)

// ItemFromLog déduit un changement de fichier d'un message rclone (niveau
// INFO/NOTICE). Les formats ont été relevés dans fs/operations (v1.76.0).
func ItemFromLog(object, msg string) (engine.Item, bool) {
	if object == "" {
		return engine.Item{}, false
	}
	it := engine.Item{Path: object, Kind: "f"}
	switch {
	case strings.HasPrefix(msg, "Copied"), strings.HasPrefix(msg, "Moved"),
		strings.HasPrefix(msg, "Skipped copy as"), strings.HasPrefix(msg, "Skipped move as"):
		it.Op = engine.ItemTransfer
	case msg == "Deleted", strings.HasPrefix(msg, "Skipped delete as"):
		it.Op = engine.ItemDelete
	case strings.HasPrefix(msg, "Made directory"), strings.HasPrefix(msg, "Skipped make directory as"):
		it.Op, it.Kind = engine.ItemCreate, "d"
	case strings.HasPrefix(msg, "Removing directory"), strings.HasPrefix(msg, "Skipped remove directory as"):
		it.Op, it.Kind = engine.ItemDelete, "d"
	case strings.HasPrefix(msg, "Updated modification time"), strings.HasPrefix(msg, "Skipped update modification time as"):
		it.Op = engine.ItemAttrs
	default:
		return engine.Item{}, false
	}
	if m := sizeRe.FindStringSubmatch(msg); m != nil {
		it.Size = parseSize(m[1], m[2])
	}
	return it, true
}

func parseSize(num, unit string) int64 {
	var f float64
	for i, c := range num {
		if c == '.' {
			frac := num[i+1:]
			div := 1.0
			for _, d := range frac {
				div *= 10
				f += float64(d-'0') / div
			}
			break
		}
		f = f*10 + float64(c-'0')
	}
	mult := map[string]float64{"": 1, "Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40, "Pi": 1 << 50}[unit]
	return int64(f * mult)
}

var redactRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(access_token|refresh_token|token|password|pass|secret|key)=([^&\s"]+)`),
	regexp.MustCompile(`(?i)(Authorization:\s*)(\S+\s+)?\S+`),
	regexp.MustCompile(`(://[^/\s:@]+:)[^@\s]+@`),
}

// Redact masque les secrets usuels dans un message de log.
func Redact(s string) string {
	s = redactRes[0].ReplaceAllString(s, "$1=***")
	s = redactRes[1].ReplaceAllString(s, "${1}***")
	s = redactRes[2].ReplaceAllString(s, "${1}***@")
	return s
}
