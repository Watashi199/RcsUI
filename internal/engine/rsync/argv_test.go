package rsync

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"rcsui/internal/engine"
)

func i(v int) *int { return &v }

func TestDecodeOptions(t *testing.T) {
	if _, err := DecodeOptions([]byte(`{"v":1,"rsyncPath":"/bin/sh"}`)); err == nil {
		t.Error("unknown field accepted")
	}
	if _, err := DecodeOptions([]byte(`{"v":1,"delete":"now"}`)); err == nil {
		t.Error("bad delete mode accepted")
	}
	if _, err := DecodeOptions([]byte(`{"v":1,"excludes":["a\nb"]}`)); err == nil {
		t.Error("newline pattern accepted")
	}
	if _, err := DecodeOptions([]byte(`{"v":1,"bwLimitKiB":-1}`)); err == nil {
		t.Error("negative bwlimit accepted")
	}
	o, err := DecodeOptions(nil)
	if err != nil || o.V != 1 {
		t.Errorf("empty options: %+v %v", o, err)
	}
	var ve *engine.ValidationError
	_, err = DecodeOptions([]byte(`{"v":1,"update":true,"ignoreExisting":true}`))
	if !errors.As(err, &ve) || ve.Fields["options.ignoreExisting"] == "" {
		t.Errorf("incompatible flags: %v", err)
	}
}

func TestBuildArgvLocal(t *testing.T) {
	o := &Options{V: 1, Delete: DeleteDuring, MaxDelete: i(50), Checksum: true, BwLimitKiB: i(1000),
		Excludes: []string{"*.tmp", "--rsync-path=evil"}, Includes: []string{"keep/**"}}
	s := engine.Spec{Mode: engine.ModeSync, DryRun: true,
		Source:      engine.Endpoint{Type: engine.EndpointLocal, Path: "/data/src", ContentsOnly: true},
		Destination: engine.Endpoint{Type: engine.EndpointLocal, Path: "/data/dst"}}
	argv, err := BuildArgv(s, o, ResolvedPaths{Source: "/data/src", Destination: "/data/dst"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-rlptD", "-c", "--delete-during", "--max-delete=50", "--bwlimit=1000",
		"--include=keep/**", "--exclude=*.tmp", "--exclude=--rsync-path=evil",
		"--info=progress2,stats2", "--no-human-readable", "--outbuf=L", "--out-format=::I %i %l %n",
		"--dry-run", "--", "/data/src/", "/data/dst/"}
	if !slices.Equal(argv, want) {
		t.Errorf("argv:\n got %q\nwant %q", argv, want)
	}
}

// Propriété de sécurité : tout ce qui vient de l'utilisateur est soit après
// "--", soit collé à une option sous la forme --opt=valeur.
func TestArgvUserDataNeverStandalone(t *testing.T) {
	evil := "-e sh -c id"
	o := &Options{V: 1, Excludes: []string{evil}, Includes: []string{evil}}
	s := engine.Spec{Mode: engine.ModeCopy,
		Source:      engine.Endpoint{Type: engine.EndpointLocal, Path: "/x"},
		Destination: engine.Endpoint{Type: engine.EndpointLocal, Path: "/y"}}
	argv, err := BuildArgv(s, o, ResolvedPaths{Source: "/x", Destination: "/y", BackupDir: "/b " + evil}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sep := slices.Index(argv, "--")
	for _, a := range argv[:sep] {
		if strings.Contains(a, evil) && !strings.HasPrefix(a, "--") {
			t.Errorf("user data as standalone arg: %q", a)
		}
		if a == "-e" {
			t.Errorf("-e present without ssh")
		}
	}
}

func TestBuildArgvSSH(t *testing.T) {
	id := int64(4)
	s := engine.Spec{Mode: engine.ModeCopy,
		Source: engine.Endpoint{Type: engine.EndpointLocal, Path: "/data/p"},
		Destination: engine.Endpoint{Type: engine.EndpointSSH, Path: "/volume1/p/",
			SSH: &engine.SSHTarget{Host: "nas2.lan", Port: 2222, User: "backup", Identity: engine.SSHIdentity{ManagedID: &id}}}}
	cfg := &SSHConfig{KeyFile: "/data/run/ssh/01ABC.key", KnownHostsFile: "/data/ssh/known_hosts"}
	argv, err := BuildArgv(s, &Options{V: 1}, ResolvedPaths{Source: "/data/p"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ei := slices.Index(argv, "-e")
	if ei < 0 {
		t.Fatal("no -e")
	}
	wantE := "ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=15 -F /dev/null -i /data/run/ssh/01ABC.key -o IdentitiesOnly=yes -o UserKnownHostsFile=/data/ssh/known_hosts -p 2222"
	if argv[ei+1] != wantE {
		t.Errorf("-e %q", argv[ei+1])
	}
	if argv[len(argv)-1] != "backup@nas2.lan:/volume1/p/" || argv[len(argv)-2] != "/data/p" {
		t.Errorf("targets %q", argv[len(argv)-2:])
	}

	bad := []engine.SSHTarget{
		{Host: "-oProxyCommand=x", Port: 22, User: "a", Identity: engine.SSHIdentity{ManagedID: &id}},
		{Host: "h", Port: 22, User: "root;id", Identity: engine.SSHIdentity{ManagedID: &id}},
		{Host: "h", Port: 0, User: "a", Identity: engine.SSHIdentity{ManagedID: &id}},
		{Host: "h", Port: 22, User: "a"},
		{Host: "h", Port: 22, User: "a", Identity: engine.SSHIdentity{ManagedID: &id, Mounted: true}},
		{Host: "h h", Port: 22, User: "a", Identity: engine.SSHIdentity{ManagedID: &id}},
	}
	for _, b := range bad {
		b := b
		s.Destination.SSH = &b
		if _, err := BuildArgv(s, &Options{V: 1}, ResolvedPaths{Source: "/data/p"}, cfg); err == nil {
			t.Errorf("accepted bad target %+v", b)
		}
	}
	s.Destination.SSH = &engine.SSHTarget{Host: "h", Port: 22, User: "a", Identity: engine.SSHIdentity{ManagedID: &id}}
	if _, err := BuildArgv(s, &Options{V: 1}, ResolvedPaths{Source: "/data/p"}, &SSHConfig{KeyFile: "/tmp/a b", KnownHostsFile: "/k"}); err == nil {
		t.Error("unsafe key path accepted")
	}
	s.Destination.Path = "-rf"
	if _, err := BuildArgv(s, &Options{V: 1}, ResolvedPaths{Source: "/data/p"}, cfg); err == nil {
		t.Error("remote path with leading dash accepted")
	}
}

func TestBuildArgvModes(t *testing.T) {
	s := engine.Spec{Mode: engine.ModeMove,
		Source:      engine.Endpoint{Type: engine.EndpointLocal, Path: "/x"},
		Destination: engine.Endpoint{Type: engine.EndpointLocal, Path: "/y"}}
	if _, err := BuildArgv(s, &Options{V: 1}, ResolvedPaths{Source: "/x", Destination: "/y"}, nil); !errors.Is(err, engine.ErrUnsupported) {
		t.Errorf("move: %v", err)
	}
	s.Mode = engine.ModeSync
	argv, _ := BuildArgv(s, &Options{V: 1}, ResolvedPaths{Source: "/x", Destination: "/y"}, nil)
	if !slices.Contains(argv, "--delete-after") {
		t.Errorf("sync default delete mode missing: %q", argv)
	}
	s.Mode = engine.ModeCopy
	argv, _ = BuildArgv(s, &Options{V: 1, Delete: DeleteDuring}, ResolvedPaths{Source: "/x", Destination: "/y"}, nil)
	for _, a := range argv {
		if strings.HasPrefix(a, "--delete") {
			t.Errorf("copy mode must not delete: %q", a)
		}
	}
}
