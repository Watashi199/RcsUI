package secrets

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSealOpen(t *testing.T) {
	b, err := New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	c, err := b.Seal([]byte("private key"), []byte("ssh-identity:1"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(c, []byte("private key")) {
		t.Fatal("plaintext visible in ciphertext")
	}
	got, err := b.Open(c, []byte("ssh-identity:1"))
	if err != nil || string(got) != "private key" {
		t.Fatalf("open: %q %v", got, err)
	}
	if _, err := b.Open(c, []byte("ssh-identity:2")); err != ErrDecrypt {
		t.Error("ciphertext accepted with other associated data")
	}
	c[len(c)-1] ^= 1
	if _, err := b.Open(c, []byte("ssh-identity:1")); err != ErrDecrypt {
		t.Error("tampered ciphertext accepted")
	}
	other, _ := New(bytes.Repeat([]byte{8}, 32))
	c2, _ := b.Seal([]byte("x"), nil)
	if _, err := other.Open(c2, nil); err != ErrDecrypt {
		t.Error("wrong key accepted")
	}
	if _, err := New([]byte("short")); err == nil {
		t.Error("short key accepted")
	}
}

func TestLoadOrCreate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "secret.key")
	k1, err := LoadOrCreate("", p)
	if err != nil || len(k1) != 32 {
		t.Fatalf("create: %v", err)
	}
	if st, _ := os.Stat(p); os.PathSeparator == '/' && st.Mode().Perm()&0o077 != 0 {
		t.Errorf("key file mode %v", st.Mode())
	}
	k2, err := LoadOrCreate("", p)
	if err != nil || !bytes.Equal(k1, k2) {
		t.Error("key not reloaded")
	}
	if _, err := LoadOrCreate("not base64!", p); err == nil {
		t.Error("invalid provided key accepted")
	}
	if _, err := LoadOrCreate("c2hvcnQ=", p); err == nil {
		t.Error("short provided key accepted")
	}
}

func TestDerive(t *testing.T) {
	master := make([]byte, 32)
	a1, _ := Derive(master, "a")
	a2, _ := Derive(master, "a")
	b, _ := Derive(master, "b")
	if len(a1) != 32 || string(a1) != string(a2) || string(a1) == string(b) {
		t.Fatal("derive must be deterministic and purpose-specific")
	}
	p1, err := RcloneConfigPassword(master)
	other := make([]byte, 32)
	other[0] = 1
	p2, _ := RcloneConfigPassword(other)
	if err != nil || len(p1) < 40 || p1 == p2 {
		t.Fatalf("rclone password: %q %q %v", p1, p2, err)
	}
}
