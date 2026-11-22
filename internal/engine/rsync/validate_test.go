package rsync

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"rcsui/internal/engine"
	"rcsui/internal/paths"
)

// Validation des options et des endpoints, sans binaire rsync (toutes plateformes).

type fakeSSH struct{ checkErr error }

func (f fakeSSH) Check(context.Context, *engine.SSHTarget) error { return f.checkErr }
func (f fakeSSH) Prepare(context.Context, string, *engine.SSHTarget) (*SSHConfig, []string, func(), error) {
	return &SSHConfig{}, nil, func() {}, nil
}

func validationEngine(t *testing.T, ssh SSHProvider) (*Engine, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := paths.New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	return &Engine{Paths: r, SSH: ssh}, root
}

func localSpec(root string, o *Options) engine.Spec {
	return engine.Spec{RunID: "V1", Kind: engine.KindRsync, Mode: engine.ModeSync, Options: o,
		Source:      engine.Endpoint{Type: engine.EndpointLocal, Path: filepath.Join(root, "src")},
		Destination: engine.Endpoint{Type: engine.EndpointLocal, Path: filepath.Join(root, "dst")}}
}

func sshTargetFor(host string) *engine.SSHTarget {
	return &engine.SSHTarget{Host: host, Port: 22, User: "backup", Identity: engine.SSHIdentity{Mounted: true}}
}

// fieldErrors retourne les champs en erreur d'une ValidationError.
func fieldErrors(t *testing.T, err error) map[string]string {
	t.Helper()
	var ve *engine.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want *engine.ValidationError, got %T (%v)", err, err)
	}
	return ve.Fields
}

func TestCheckPattern(t *testing.T) {
	cases := map[string]string{
		"*.tmp":                   "",
		"":                        "empty pattern",
		strings.Repeat("a", 1025): "pattern too long",
		"a\nb":                    "pattern contains control characters",
		"a\x00b":                  "pattern contains control characters",
		"a\rb":                    "pattern contains control characters",
	}
	for p, want := range cases {
		if got := checkPattern(p); got != want {
			t.Errorf("checkPattern(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestOptionsValidate(t *testing.T) {
	neg := -1
	big := 86401
	many := make([]string, maxPatterns+1)
	for i := range many {
		many[i] = "x"
	}
	cases := []struct {
		name  string
		o     Options
		field string
	}{
		{"version", Options{V: 2}, "options.v"},
		{"delete mode", Options{V: 1, Delete: "sometimes"}, "options.delete"},
		{"negative maxDelete", Options{V: 1, MaxDelete: &neg}, "options.maxDelete"},
		{"timeout too long", Options{V: 1, TimeoutSec: &big}, "options.timeoutSec"},
		{"update and ignoreExisting", Options{V: 1, Update: true, IgnoreExisting: true}, "options.ignoreExisting"},
		{"existingOnly and ignoreExisting", Options{V: 1, ExistingOnly: true, IgnoreExisting: true}, "options.existingOnly"},
		{"too many patterns", Options{V: 1, Excludes: many}, "options.excludes"},
		{"bad exclude", Options{V: 1, Excludes: []string{"ok", ""}}, "options.excludes.1"},
		{"bad include", Options{V: 1, Includes: []string{"a\nb"}}, "options.includes.0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := fieldErrors(t, c.o.validate())
			if _, ok := f[c.field]; !ok {
				t.Errorf("fields %v: missing %q", f, c.field)
			}
		})
	}
	ok := Options{V: 1, Delete: DeleteAfter, Excludes: []string{"*.tmp"}, Includes: []string{"keep/**"}}
	if err := ok.validate(); err != nil {
		t.Errorf("valid options rejected: %v", err)
	}
}

func TestOptionsWarnings(t *testing.T) {
	o := Options{V: 1, Owner: true}
	if w := o.Warnings(true); len(w) != 1 {
		t.Errorf("non-root with owner: warnings=%v", w)
	}
	if w := o.Warnings(false); len(w) != 0 {
		t.Errorf("root: warnings=%v", w)
	}
	if w := (&Options{V: 1}).Warnings(true); len(w) != 0 {
		t.Errorf("no owner/group: warnings=%v", w)
	}
}

func TestDeleteModeString(t *testing.T) {
	if got := DeleteAfter.String(); got != `"after"` {
		t.Errorf("String() = %s", got)
	}
}

func TestEngineDecodeOptions(t *testing.T) {
	e := &Engine{}
	if e.Kind() != engine.KindRsync {
		t.Errorf("kind = %s", e.Kind())
	}
	v, err := e.DecodeOptions([]byte(`{"v":1,"delete":"after"}`))
	if err != nil {
		t.Fatal(err)
	}
	if o, ok := v.(*Options); !ok || o.Delete != DeleteAfter {
		t.Errorf("decoded %#v", v)
	}
	if _, err := e.DecodeOptions([]byte(`{"v":1,"shell":"sh"}`)); err == nil {
		t.Error("unknown field accepted")
	}
}

func TestValidateRejects(t *testing.T) {
	ctx := context.Background()
	e, root := validationEngine(t, nil)
	o := &Options{V: 1}

	t.Run("options not decoded", func(t *testing.T) {
		s := localSpec(root, nil)
		if err := e.Validate(ctx, s); err == nil || !strings.Contains(err.Error(), "not decoded") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("resync", func(t *testing.T) {
		s := localSpec(root, o)
		s.Resync = true
		if _, ok := fieldErrors(t, e.Validate(ctx, s))["resync"]; !ok {
			t.Error("resync accepted")
		}
	})
	t.Run("mode", func(t *testing.T) {
		s := localSpec(root, o)
		s.Mode = engine.ModeBisync
		if _, ok := fieldErrors(t, e.Validate(ctx, s))["mode"]; !ok {
			t.Error("bisync accepted")
		}
	})
	t.Run("endpoint type", func(t *testing.T) {
		s := localSpec(root, o)
		s.Source = engine.Endpoint{Type: engine.EndpointRemote, Path: "s3:bucket"}
		if _, ok := fieldErrors(t, e.Validate(ctx, s))["source.type"]; !ok {
			t.Error("rclone remote accepted")
		}
	})
	t.Run("destination inside source", func(t *testing.T) {
		s := localSpec(root, o)
		s.Destination.Path = filepath.Join(root, "src", "copy")
		if _, ok := fieldErrors(t, e.Validate(ctx, s))["destination.path"]; !ok {
			t.Error("nested destination accepted")
		}
	})
	t.Run("outside allowed roots", func(t *testing.T) {
		outside, _ := filepath.EvalSymlinks(t.TempDir())
		s := localSpec(root, &Options{V: 1, BackupDir: outside, LinkDest: outside})
		f := fieldErrors(t, e.Validate(ctx, s))
		for _, k := range []string{"options.backupDir", "options.linkDest"} {
			if _, ok := f[k]; !ok {
				t.Errorf("fields %v: missing %q", f, k)
			}
		}
	})
	t.Run("ssh unavailable", func(t *testing.T) {
		s := localSpec(root, o)
		s.Destination = engine.Endpoint{Type: engine.EndpointSSH, Path: "/backup", SSH: sshTargetFor("nas.lan")}
		if _, ok := fieldErrors(t, e.Validate(ctx, s))["destination.type"]; !ok {
			t.Error("ssh accepted without provider")
		}
	})
}

func TestValidateSSHEndpoints(t *testing.T) {
	ctx := context.Background()
	e, root := validationEngine(t, fakeSSH{})
	o := &Options{V: 1}
	remote := func(path string, target *engine.SSHTarget) engine.Spec {
		s := localSpec(root, o)
		s.Destination = engine.Endpoint{Type: engine.EndpointSSH, Path: path, SSH: target}
		return s
	}

	if err := e.Validate(ctx, remote("/backup", sshTargetFor("nas.lan"))); err != nil {
		t.Fatalf("valid ssh destination rejected: %v", err)
	}
	cases := []struct {
		name  string
		s     engine.Spec
		field string
	}{
		{"target missing", remote("/backup", nil), "destination.ssh"},
		{"bad host", remote("/backup", sshTargetFor("-oProxyCommand=x")), "destination.ssh"},
		{"bad remote path", remote("-e sh", sshTargetFor("nas.lan")), "destination.path"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := fieldErrors(t, e.Validate(ctx, c.s))[c.field]; !ok {
				t.Errorf("missing %q", c.field)
			}
		})
	}
	t.Run("remote to remote", func(t *testing.T) {
		s := remote("/backup", sshTargetFor("nas.lan"))
		s.Source = engine.Endpoint{Type: engine.EndpointSSH, Path: "/data", SSH: sshTargetFor("nas2.lan")}
		if _, ok := fieldErrors(t, e.Validate(ctx, s))["destination.type"]; !ok {
			t.Error("remote to remote accepted")
		}
	})
	t.Run("host not enrolled", func(t *testing.T) {
		e2, _ := validationEngine(t, fakeSSH{checkErr: errors.New("not in known hosts")})
		if _, ok := fieldErrors(t, e2.Validate(ctx, remote("/backup", sshTargetFor("nas.lan"))))["destination.ssh"]; !ok {
			t.Error("unknown host accepted")
		}
	})
}

func TestPreview(t *testing.T) {
	e, root := validationEngine(t, fakeSSH{})
	p, err := e.Preview(localSpec(root, &Options{V: 1, Delete: DeleteAfter}))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Argv) < 3 || p.Argv[0] != "rsync" || !strings.Contains(strings.Join(p.Argv, " "), "--delete-after") {
		t.Errorf("argv = %v", p.Argv)
	}

	e.Binary = "/usr/bin/rsync"
	s := localSpec(root, &Options{V: 1})
	s.Destination = engine.Endpoint{Type: engine.EndpointSSH, Path: "/backup", SSH: sshTargetFor("nas.lan")}
	p, err = e.Preview(s)
	if err != nil {
		t.Fatal(err)
	}
	if p.Argv[0] != "/usr/bin/rsync" || !strings.Contains(strings.Join(p.Argv, " "), "backup@nas.lan:") {
		t.Errorf("ssh argv = %v", p.Argv)
	}

	if _, err := e.Preview(localSpec(root, nil)); err == nil {
		t.Error("preview without options accepted")
	}
	bad := localSpec(root, &Options{V: 1})
	bad.Mode = engine.ModeBisync
	if _, err := e.Preview(bad); err == nil {
		t.Error("preview of an invalid spec accepted")
	}
}
