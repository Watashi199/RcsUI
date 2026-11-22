package rclone

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/accounting"

	"rcsui/internal/engine"
)

// logRouter associe un groupe de stats ("run/<id>") au Sink du run.
//
// Bisync journalise son récit (« Building Path1 and Path2 listings »,
// « Applying changes »…) sans contexte, donc sans groupe. Quand un seul bisync
// est en cours, ces messages lui sont attribués en direct ; avec plusieurs
// bisyncs simultanés, l'attribution serait ambiguë et le run retombe sur le
// champ "output" du résultat rc (messages NOTICE et plus, en fin de run).
type logRouter struct {
	mu      sync.RWMutex
	sinks   map[string]engine.Sink
	bisync  map[string]bool           // groupes des bisyncs en cours
	overlap map[string]bool           // bisyncs ayant tourné en même temps qu'un autre
	live    map[string]map[string]int // messages sans groupe reçus en direct, par bisync
}

func newLogRouter() *logRouter {
	return &logRouter{sinks: map[string]engine.Sink{}, bisync: map[string]bool{},
		overlap: map[string]bool{}, live: map[string]map[string]int{}}
}

func (r *logRouter) register(group string, s engine.Sink, bisync bool) {
	r.mu.Lock()
	r.sinks[group] = s
	if bisync {
		// Chevauchement : l'attribution en direct s'interrompt pour tous ;
		// chacun rejouera en fin de run ce qu'il n'a pas reçu (pendingOutput).
		if len(r.bisync) > 0 {
			for g := range r.bisync {
				r.overlap[g] = true
			}
			r.overlap[group] = true
		}
		r.bisync[group] = true
	}
	r.mu.Unlock()
}

func (r *logRouter) unregister(group string) {
	r.mu.Lock()
	delete(r.sinks, group)
	delete(r.bisync, group)
	delete(r.overlap, group)
	delete(r.live, group)
	r.mu.Unlock()
}

func (r *logRouter) get(group string) engine.Sink {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sinks[group]
}

// soleBisync retourne le bisync en cours s'il est seul (sinon "", nil).
func (r *logRouter) soleBisync() (string, engine.Sink) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.bisync) != 1 {
		return "", nil
	}
	for g := range r.bisync {
		return g, r.sinks[g]
	}
	return "", nil
}

func (r *logRouter) markLive(group, msg string) {
	r.mu.Lock()
	if r.live[group] == nil {
		r.live[group] = map[string]int{}
	}
	r.live[group][msg]++
	r.mu.Unlock()
}

// pendingOutput filtre les lignes du champ "output" d'un bisync à rejouer en
// fin de run : toutes s'il n'a rien reçu en direct, aucune s'il a tout reçu
// (seul bisync du début à la fin), sinon celles qu'il n'a pas reçues.
func (r *logRouter) pendingOutput(group string, lines []engine.LogLine) []engine.LogLine {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := r.live[group]
	if len(seen) == 0 {
		return lines
	}
	if !r.overlap[group] {
		return nil
	}
	var out []engine.LogLine
	for _, l := range lines {
		if seen[l.Msg] > 0 {
			seen[l.Msg]--
			continue
		}
		out = append(out, l)
	}
	return out
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

// noticeLevel : le niveau global de rclone est INFO (Init) pour que le récit de
// bisync ne soit pas filtré avant d'arriver ici ; les messages sans run en
// dessous de NOTICE sont jetés pour garder le log serveur aussi sobre qu'avant.
const noticeLevel = fs.SlogLevelNotice

func (h *logHandler) Handle(ctx context.Context, r slog.Record) error {
	if group, ok := accounting.StatsGroupFromContext(ctx); ok {
		if sink := h.router.get(group); sink != nil {
			deliver(sink, r)
			return nil
		}
	}
	if r.Level >= slog.LevelInfo {
		if g, sink := h.router.soleBisync(); sink != nil {
			h.router.markLive(g, deliver(sink, r))
			// Transmis aussi à rclone : bisync le capture dans "output", où
			// l'engine cherche les consignes de resync.
			return h.inner.Handle(ctx, r)
		}
	}
	if r.Level < noticeLevel {
		return nil
	}
	return h.inner.Handle(ctx, r)
}

// deliver écrit un enregistrement rclone dans le log du run et retourne le
// message tel qu'écrit (comparé ensuite aux lignes de "output", cf. pendingOutput).
func deliver(sink engine.Sink, r slog.Record) string {
	object := ""
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "object" {
			object = a.Value.String()
		}
		return true
	})
	// Bisync colore ses messages (codes ANSI) : inutiles dans le log du run.
	msg := Redact(ansiRe.ReplaceAllString(r.Message, ""))
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
	return full
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
