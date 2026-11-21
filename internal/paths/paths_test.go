package paths

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func setup(t *testing.T) (root, outside string, r *Resolver) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "root")
	outside = filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(root, "a", "b"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(root, "a", "f.txt"), []byte("x"), 0o644)
	var err error
	r, err = New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	root, _ = filepath.EvalSymlinks(root)
	outside, _ = filepath.EvalSymlinks(outside)
	return root, outside, r
}

func TestResolve(t *testing.T) {
	root, outside, r := setup(t)
	ok := []string{root, filepath.Join(root, "a"), filepath.Join(root, "a", "f.txt"), filepath.Join(root, "new", "deep", "dir")}
	for _, p := range ok {
		if _, err := r.Resolve(p); err != nil {
			t.Errorf("Resolve(%q): %v", p, err)
		}
	}
	bad := map[string]error{
		outside:                              ErrOutsideRoots,
		filepath.Join(root, "..", "outside"): ErrOutsideRoots,
		filepath.Join(root, "a", "..", ".."): ErrOutsideRoots,
		"relative/path":                      ErrInvalid,
		"-rf":                                ErrInvalid,
		"":                                   ErrInvalid,
		filepath.Join(root, "x\x00y"):        ErrInvalid,
		filepath.Join(root, "line\nbreak"):   ErrInvalid,
		root + "sibling":                     ErrOutsideRoots, // préfixe commun sans séparateur
	}
	for p, want := range bad {
		if _, err := r.Resolve(p); !errors.Is(err, want) {
			t.Errorf("Resolve(%q) = %v, want %v", p, err, want)
		}
	}
}

func TestResolveSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require privileges on Windows")
	}
	root, outside, r := setup(t)
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(filepath.Join(root, "escape", "x")); !errors.Is(err, ErrOutsideRoots) {
		t.Errorf("symlink escape not detected: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "inside")); err != nil {
		t.Fatal(err)
	}
	got, err := r.Resolve(filepath.Join(root, "inside", "f.txt"))
	if err != nil || got != filepath.Join(root, "a", "f.txt") {
		t.Errorf("inside link: %q %v", got, err)
	}
	os.Symlink(filepath.Join(root, "loop"), filepath.Join(root, "loop"))
	if _, err := r.Resolve(filepath.Join(root, "loop", "x")); err == nil {
		t.Error("symlink loop accepted")
	}
	l, err := r.List(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range l.Entries {
		if e.Name == "escape" && e.Dir {
			t.Error("escaping symlink shown as navigable dir")
		}
	}
}

func TestList(t *testing.T) {
	root, outside, r := setup(t)
	os.WriteFile(filepath.Join(root, "z.txt"), nil, 0o644)
	l, err := r.List(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != 2 || l.Entries[0].Name != "a" || !l.Entries[0].Dir || l.Entries[1].Name != "z.txt" {
		t.Errorf("entries=%+v", l.Entries)
	}
	l, _ = r.List(root, 1)
	if !l.Truncated || len(l.Entries) != 1 {
		t.Errorf("truncation: %+v", l)
	}
	if _, err := r.List(outside, 0); !errors.Is(err, ErrOutsideRoots) {
		t.Errorf("outside list: %v", err)
	}
	sub, err := r.List(filepath.Join(root, "a"), 0)
	if err != nil || len(sub.Entries) != 2 {
		t.Errorf("sub list: %+v %v", sub, err)
	}
}

func TestNewRejectsBadRoots(t *testing.T) {
	if _, err := New([]string{"relative"}); err == nil {
		t.Error("relative root accepted")
	}
	if _, err := New([]string{filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Error("missing root accepted")
	}
	r, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(t.TempDir()); !errors.Is(err, ErrOutsideRoots) {
		t.Errorf("no roots should deny all: %v", err)
	}
}
