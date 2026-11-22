package rclone

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"

	"rcsui/internal/engine"
)

type innerRec struct {
	mu   sync.Mutex
	msgs []string
}

func (h *innerRec) Enabled(context.Context, slog.Level) bool { return true }
func (h *innerRec) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.msgs = append(h.msgs, r.Message)
	h.mu.Unlock()
	return nil
}
func (h *innerRec) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *innerRec) WithGroup(string) slog.Handler      { return h }

type sinkRec struct {
	mu   sync.Mutex
	logs []engine.LogLine
}

func (s *sinkRec) OnProgress(engine.Progress) {}
func (s *sinkRec) OnItem(engine.Item)         {}
func (s *sinkRec) OnLog(l engine.LogLine)     { s.mu.Lock(); s.logs = append(s.logs, l); s.mu.Unlock() }

func record(level slog.Level, msg string) slog.Record {
	return slog.NewRecord(time.Now(), level, msg, 0)
}

// Messages sans groupe de stats (récit de bisync, messages globaux).
func TestLogRouterUngrouped(t *testing.T) {
	ctx := context.Background()
	inner := &innerRec{}
	router := newLogRouter()
	h := &logHandler{inner: inner, router: router}

	// Aucun bisync : INFO jeté (log serveur inchangé), NOTICE transmis.
	_ = h.Handle(ctx, record(slog.LevelInfo, "info"))
	_ = h.Handle(ctx, record(fs.SlogLevelNotice, "notice"))
	if len(inner.msgs) != 1 || inner.msgs[0] != "notice" {
		t.Fatalf("inner got %v, want [notice]", inner.msgs)
	}

	// Un seul bisync : attribué en direct, et transmis à rclone (capture "output").
	bis, other := &sinkRec{}, &sinkRec{}
	router.register("run/b1", bis, true)
	router.register("run/c1", other, false)
	_ = h.Handle(ctx, record(slog.LevelInfo, "Applying changes"))
	_ = h.Handle(ctx, record(slog.LevelDebug, "debug"))
	if len(bis.logs) != 1 || bis.logs[0].Msg != "Applying changes" || len(other.logs) != 0 {
		t.Fatalf("bisync=%+v other=%+v", bis.logs, other.logs)
	}
	if inner.msgs[len(inner.msgs)-1] != "Applying changes" {
		t.Errorf("not forwarded to rclone: %v", inner.msgs)
	}
	output := []engine.LogLine{{Msg: "Applying changes"}, {Msg: "Bisync successful"}}
	// Seul du début à la fin : tout a été reçu en direct, rien à rejouer.
	if got := router.pendingOutput("run/b1", output); len(got) != 0 {
		t.Errorf("sole bisync replays %v", got)
	}
	// Un run qui n'a rien reçu en direct rejoue tout.
	if got := router.pendingOutput("run/c1", output); len(got) != 2 {
		t.Errorf("no live delivery: replays %v", got)
	}

	// Deux bisyncs : ambigu, rien n'est attribué...
	bis2 := &sinkRec{}
	router.register("run/b2", bis2, true)
	_ = h.Handle(ctx, record(slog.LevelInfo, "ambiguous"))
	if len(bis.logs) != 1 || len(bis2.logs) != 0 {
		t.Fatalf("ambiguous message attributed: %+v %+v", bis.logs, bis2.logs)
	}
	// ...et le premier rejoue ce qu'il n'a pas reçu pendant le chevauchement (sans doublon).
	got := router.pendingOutput("run/b1", append(output, engine.LogLine{Msg: "ambiguous"}))
	if len(got) != 2 || got[0].Msg != "Bisync successful" || got[1].Msg != "ambiguous" {
		t.Errorf("overlapped bisync replays %+v", got)
	}

	router.unregister("run/b1")
	router.unregister("run/b2")
	if got := router.pendingOutput("run/b1", output); len(got) != 2 {
		t.Error("state kept after unregister")
	}
}
