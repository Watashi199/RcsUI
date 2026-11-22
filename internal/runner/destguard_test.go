package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rcsui/internal/engine"
	"rcsui/internal/store"
)

func local(p string) engine.Endpoint { return engine.Endpoint{Type: engine.EndpointLocal, Path: p} }

func endpointsOverlap(a, b engine.Endpoint) bool {
	fa, pa := location(a)
	fb, pb := location(b)
	return place{fa, pa}.overlaps(place{fb, pb})
}

func writesOverlap(a, b engine.Spec) bool {
	return placesOverlap(writtenPlaces(a.Mode, a.Source, a.Destination), writtenPlaces(b.Mode, b.Source, b.Destination))
}

// Deux chemins vers le même dossier via un symlink se chevauchent (les moteurs
// résolvent les symlinks avant d'écrire).
func TestOverlapThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "backup")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if !endpointsOverlap(local(real), local(filepath.Join(alias, "photos"))) {
		t.Error("overlap through a symlink not detected")
	}
	if endpointsOverlap(local(filepath.Join(dir, "other")), local(alias)) {
		t.Error("unrelated path flagged")
	}
}

func TestEndpointsOverlap(t *testing.T) {
	ssh := func(host string, port int, p string) engine.Endpoint {
		return engine.Endpoint{Type: engine.EndpointSSH, Path: p, SSH: &engine.SSHTarget{Host: host, Port: port}}
	}
	remote := func(r, p string) engine.Endpoint {
		return engine.Endpoint{Type: engine.EndpointRemote, Remote: r, Path: p}
	}
	cases := []struct {
		a, b engine.Endpoint
		want bool
	}{
		{local("/data/b"), local("/data/b/"), true},
		{local("/data/b"), local("/data/b/sub"), true},
		{local("/data/b/sub"), local("/data/b"), true},
		{local("/data/b"), local("/data/bc"), false},
		{local("/data/b"), local("/data/c"), false},
		{local(`C:\data\b`), local("C:/data/b/x"), true},
		{local("/"), local("/anything"), true},
		{remote("s3", ""), remote("s3", "bucket/x"), true},
		{remote("s3", "bucket/x"), remote("s3", "bucket/y"), false},
		{remote("s3", "bucket"), remote("gdrive", "bucket"), false},
		{remote("s3", "/b"), local("/b"), false},
		{ssh("NAS", 0, "/b"), ssh("nas", 22, "/b/x"), true},
		{ssh("nas", 22, "/b"), ssh("nas", 2222, "/b"), false},
		{ssh("nas", 22, "/b"), local("/b"), false},
	}
	for _, c := range cases {
		if got := endpointsOverlap(c.a, c.b); got != c.want {
			t.Errorf("overlap(%+v, %+v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestWritesOverlap(t *testing.T) {
	spec := func(m engine.Mode, src, dst string) engine.Spec {
		return engine.Spec{Mode: m, Source: local(src), Destination: local(dst)}
	}
	// Destinations communes.
	if !writesOverlap(spec(engine.ModeCopy, "/a", "/out"), spec(engine.ModeSync, "/b", "/out/x")) {
		t.Error("same destination tree not detected")
	}
	// Lire ce qu'un autre écrit n'est pas un conflit d'écriture.
	if writesOverlap(spec(engine.ModeCopy, "/out", "/c"), spec(engine.ModeCopy, "/a", "/out")) {
		t.Error("copy reading another job's destination flagged")
	}
	// move supprime la source : elle est écrite.
	if !writesOverlap(spec(engine.ModeMove, "/out", "/c"), spec(engine.ModeCopy, "/a", "/out")) {
		t.Error("move source not treated as written")
	}
	// bisync écrit des deux côtés.
	if !writesOverlap(spec(engine.ModeCopy, "/a", "/p1"), spec(engine.ModeBisync, "/p1", "/p2")) {
		t.Error("bisync path1 not treated as written")
	}
}

func (e *env) jobAt(t *testing.T, name, dst string, exclusive bool) int64 {
	t.Helper()
	id, err := e.db.CreateJob(context.Background(), store.Job{Name: name, Engine: engine.KindRsync, Mode: engine.ModeCopy,
		Source: local("/src/" + name), Destination: local(dst), Options: []byte(`{"v":1}`), ExclusiveDest: exclusive})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestExclusiveDestination(t *testing.T) {
	e := newEnv(t, 4)
	ctx := context.Background()
	writer := e.jobAt(t, "writer", "/backup", false)
	guarded := e.jobAt(t, "guarded", "/backup/photos", true)
	unguarded := e.jobAt(t, "unguarded", "/backup/docs", false)
	elsewhere := e.jobAt(t, "elsewhere", "/other", true)

	id, err := e.m.Submit(ctx, writer, store.TriggerManual, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	x := e.nextStart(t)

	// Le job exclusif ne démarre pas, l'erreur nomme le job qui occupe la destination.
	_, err = e.m.Submit(ctx, guarded, store.TriggerManual, nil, false)
	var busy *DestinationBusyError
	if !errors.Is(err, ErrDestinationBusy) || !errors.As(err, &busy) || busy.JobName != "writer" {
		t.Fatalf("guarded submit: %v", err)
	}
	if e.m.IsJobActive(guarded) {
		t.Fatal("guarded job registered as active")
	}
	// L'option est désactivée par défaut : sans elle, le chevauchement est permis.
	if _, err := e.m.Submit(ctx, unguarded, store.TriggerManual, nil, false); err != nil {
		t.Fatalf("unguarded submit: %v", err)
	}
	e.nextStart(t).finish(engine.Result{Outcome: engine.OutcomeSucceeded})
	// Un job exclusif sans chevauchement démarre normalement.
	if _, err := e.m.Submit(ctx, elsewhere, store.TriggerManual, nil, false); err != nil {
		t.Fatalf("elsewhere submit: %v", err)
	}
	e.nextStart(t).finish(engine.Result{Outcome: engine.OutcomeSucceeded})

	// Destination libérée : le job exclusif démarre.
	x.finish(engine.Result{Outcome: engine.OutcomeSucceeded})
	waitState(t, e.db, id, store.StateSucceeded)
	deadline := time.Now().Add(3 * time.Second)
	for e.m.IsJobActive(writer) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := e.m.Submit(ctx, guarded, store.TriggerManual, nil, false); err != nil {
		t.Fatalf("guarded submit after release: %v", err)
	}
	e.nextStart(t).finish(engine.Result{Outcome: engine.OutcomeSucceeded})
}

// Une relance automatique bloquée par la destination est enregistrée « skipped »
// et n'est plus en attente.
func TestRetrySkippedWhenDestinationBusy(t *testing.T) {
	e := newEnv(t, 4)
	e.m.d.RetryUnit = time.Millisecond
	ctx := context.Background()
	guarded := e.jobAt(t, "guarded", "/backup", true)
	e.setRetry(t, guarded, 2, 300) // 300 ms : le temps d'occuper la destination
	writer := e.jobAt(t, "writer", "/backup/sub", false)

	e.failRetryable(t, guarded)
	waitPendingRetry(t, e.m, guarded)
	if _, err := e.m.Submit(ctx, writer, store.TriggerManual, nil, false); err != nil {
		t.Fatal(err)
	}
	x := e.nextStart(t)
	defer x.finish(engine.Result{Outcome: engine.OutcomeSucceeded})

	deadline := time.Now().Add(3 * time.Second)
	for {
		runs, err := e.db.ListRuns(ctx, store.RunFilter{JobID: &guarded, States: []store.RunState{store.StateSkipped}})
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) == 1 {
			r := runs[0]
			if r.Trigger != store.TriggerRetry || r.Attempt != 2 || r.ErrorMessage != `destination in use by job "writer"` {
				t.Fatalf("skipped retry run: %+v", r)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no skipped retry run recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for len(e.pendingRows(t)) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("pending retry kept: %+v", e.pendingRows(t))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
