package rclone

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rcsui/internal/engine"
)

func bisyncSpec(t *testing.T, root string, n int, opts string, resync, dry bool) engine.Spec {
	t.Helper()
	o, err := DecodeOptions([]byte(opts))
	if err != nil {
		t.Fatal(err)
	}
	return engine.Spec{RunID: strings.ReplaceAll(t.Name(), "/", "_") + "-" + string(rune('a'+n)), Kind: engine.KindRclone,
		Mode: engine.ModeBisync, DryRun: dry, Resync: resync, Options: o,
		Source:      engine.Endpoint{Type: engine.EndpointLocal, Path: filepath.Join(root, "p1")},
		Destination: engine.Endpoint{Type: engine.EndpointLocal, Path: filepath.Join(root, "p2")}}
}

func setupBisync(t *testing.T) (*Engine, string) {
	e, root := setup(t)
	e.BisyncDir = filepath.Join(t.TempDir(), "bisync")
	for _, d := range []string{"p1", "p2"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return e, root
}

func write(t *testing.T, p, content string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestBisyncLifecycle(t *testing.T) {
	e, root := setupBisync(t)
	p1, p2 := filepath.Join(root, "p1"), filepath.Join(root, "p2")
	old := time.Now().Add(-2 * time.Hour)
	write(t, filepath.Join(p1, "a.txt"), "a1", old)
	write(t, filepath.Join(p2, "b.txt"), "b2", old)

	// 1. Sans listings de référence : échec, non relançable, avec indication.
	res, r := run(t, e, bisyncSpec(t, root, 0, `{"v":1}`, false, false))
	if res.Outcome != engine.OutcomeFailed || res.Retryable {
		t.Fatalf("first run without resync: %+v", res)
	}
	hint, cause := false, false
	for _, l := range r.logs {
		if l.Stream == "system" && strings.Contains(l.Msg, "resync") {
			hint = true
		}
		// Message capturé par bisync (output du résultat rc), horodatage retiré.
		if l.Stream == "rclone" && l.Level == engine.LevelError && strings.HasPrefix(l.Msg, "Bisync critical error") {
			cause = true
		}
	}
	if !hint || !cause {
		t.Errorf("missing resync hint (%v) or bisync error (%v) in logs: %+v", hint, cause, r.logs)
	}

	// 2. Resync : union des deux côtés, rien n'est supprimé.
	res, r = run(t, e, bisyncSpec(t, root, 1, `{"v":1}`, true, false))
	if res.Outcome != engine.OutcomeSucceeded {
		t.Fatalf("resync: %+v logs=%+v", res, r.logs)
	}
	if read(t, filepath.Join(p2, "a.txt")) != "a1" || read(t, filepath.Join(p1, "b.txt")) != "b2" {
		t.Fatal("resync did not merge both sides")
	}

	// 3. Changements des deux côtés, propagés dans les deux sens.
	newer := time.Now().Add(-time.Hour)
	write(t, filepath.Join(p1, "c.txt"), "c1", newer)
	write(t, filepath.Join(p2, "a.txt"), "a2-updated", newer)
	if err := os.Remove(filepath.Join(p1, "b.txt")); err != nil {
		t.Fatal(err)
	}

	// 3a. Dry-run : aucune modification.
	res, _ = run(t, e, bisyncSpec(t, root, 2, `{"v":1}`, false, true))
	if res.Outcome != engine.OutcomeSucceeded {
		t.Fatalf("dry-run: %+v", res)
	}
	if read(t, filepath.Join(p2, "c.txt")) != "<missing>" || read(t, filepath.Join(p2, "b.txt")) != "b2" {
		t.Fatal("dry-run modified path2")
	}

	// 3b. Run réel.
	res, r = run(t, e, bisyncSpec(t, root, 3, `{"v":1}`, false, false))
	if res.Outcome != engine.OutcomeSucceeded {
		t.Fatalf("bisync: %+v logs=%+v", res, r.logs)
	}
	if got := read(t, filepath.Join(p2, "c.txt")); got != "c1" {
		t.Errorf("new file not copied to path2: %q", got)
	}
	if got := read(t, filepath.Join(p1, "a.txt")); got != "a2-updated" {
		t.Errorf("update not copied to path1: %q", got)
	}
	if got := read(t, filepath.Join(p2, "b.txt")); got != "<missing>" {
		t.Errorf("deletion not propagated to path2: %q", got)
	}

	// 4. Filtres modifiés : bisync exige un resync au lieu de supprimer.
	res, _ = run(t, e, bisyncSpec(t, root, 4, `{"v":1,"excludes":["c.txt"]}`, false, false))
	if res.Outcome != engine.OutcomeFailed || res.Retryable {
		t.Fatalf("changed filters must require a resync: %+v", res)
	}
	if read(t, filepath.Join(p1, "c.txt")) != "c1" || read(t, filepath.Join(p2, "c.txt")) != "c1" {
		t.Fatal("changed filters deleted files")
	}
}

func TestBisyncValidation(t *testing.T) {
	e, root := setupBisync(t)
	cases := map[string]struct {
		mode   engine.Mode
		opts   string
		resync bool
		field  string
	}{
		"bisync options in copy":   {engine.ModeCopy, `{"v":1,"bisync":{}}`, false, "options.bisync"},
		"resync in copy":           {engine.ModeCopy, `{"v":1}`, true, "resync"},
		"checksum in bisync":       {engine.ModeBisync, `{"v":1,"checksum":true}`, false, "options.checksum"},
		"maxDelete count":          {engine.ModeBisync, `{"v":1,"maxDelete":3}`, false, "options.maxDelete"},
		"age filter":               {engine.ModeBisync, `{"v":1,"maxAge":"7d"}`, false, "options.maxAge"},
		"trackRenames":             {engine.ModeBisync, `{"v":1,"trackRenames":true}`, false, "options"},
		"source inside path2 tree": {engine.ModeBisync, `{"v":1}`, false, "source.path"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := bisyncSpec(t, root, 0, c.opts, c.resync, false)
			s.Mode = c.mode
			if name == "source inside path2 tree" {
				s.Source.Path = filepath.Join(root, "p2", "sub")
				os.MkdirAll(s.Source.Path, 0o755)
			}
			err := e.Validate(context.Background(), s)
			var ve *engine.ValidationError
			if !errors.As(err, &ve) || ve.Fields[c.field] == "" {
				t.Fatalf("want error on %q, got %v", c.field, err)
			}
		})
	}

	for _, bad := range []string{
		`{"v":1,"bisync":{"conflictResolve":"random"}}`,
		`{"v":1,"bisync":{"conflictLoser":"keep"}}`,
		`{"v":1,"bisync":{"resyncMode":"both"}}`,
		`{"v":1,"bisync":{"compare":"size,hash"}}`,
		`{"v":1,"bisync":{"maxDeletePercent":101}}`,
		`{"v":1,"bisync":{"conflictSuffix":"a/b"}}`,
		`{"v":1,"bisync":{"maxLock":"forever"}}`,
	} {
		if _, err := DecodeOptions([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}

	e.BisyncDir = ""
	if err := e.Validate(context.Background(), bisyncSpec(t, root, 0, `{"v":1}`, false, false)); err == nil {
		t.Error("bisync accepted without a working directory")
	}
}

func TestBisyncPreview(t *testing.T) {
	e, root := setupBisync(t)
	s := bisyncSpec(t, root, 0, `{"v":1,"excludes":["*.tmp"],"bisync":{"conflictResolve":"newer","maxDeletePercent":10}}`, true, false)
	p, err := e.Preview(s)
	if err != nil {
		t.Fatal(err)
	}
	if p.RPC != "sync/bisync" || p.In["resync"] != true || p.In["conflictResolve"] != "newer" || p.In["maxDelete"] != 10 ||
		p.In["recover"] != true || p.In["workdir"] != e.BisyncDir {
		t.Fatalf("preview: %+v", p)
	}
	f1, _ := p.In["filtersFile"].(string)
	s2 := bisyncSpec(t, root, 0, `{"v":1,"excludes":["*.bak"]}`, false, false)
	p2, _ := e.Preview(s2)
	if f2, _ := p2.In["filtersFile"].(string); f1 == "" || f1 == f2 || !strings.HasPrefix(f1, e.BisyncDir) {
		t.Fatalf("filters file must depend on filters: %q vs %q", f1, f2)
	}
}
