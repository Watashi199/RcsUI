package backup

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSealOpen(t *testing.T) {
	plain := []byte(`{"jobs":[{"name":"photos"}],"secret":"s3cr3t"}`)
	pass := "correct horse battery"
	file, err := Seal(plain, pass)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(file), "s3cr3t") || strings.Contains(string(file), "photos") {
		t.Fatal("plaintext visible in the export")
	}
	got, err := Open(file, pass)
	if err != nil || string(got) != string(plain) {
		t.Fatalf("open: %q %v", got, err)
	}
	if _, err := Open(file, "wrong passphrase!"); !errors.Is(err, ErrPassphrase) {
		t.Errorf("wrong passphrase: %v", err)
	}

	// Paramètres de dérivation altérés : refusés (authentifiés par l'AD).
	var env Envelope
	_ = json.Unmarshal(file, &env)
	env.KDF.Time = 4
	tampered, _ := json.Marshal(env)
	if _, err := Open(tampered, pass); !errors.Is(err, ErrPassphrase) {
		t.Errorf("tampered KDF accepted: %v", err)
	}
	// Paramètres hors bornes : refusés avant tout calcul.
	env.KDF.MemoryKiB = 4 << 20
	huge, _ := json.Marshal(env)
	if _, err := Open(huge, pass); err == nil || errors.Is(err, ErrPassphrase) {
		t.Errorf("oversized KDF: %v", err)
	}
	for _, bad := range []string{`not json`, `{"format":"other"}`, `{"format":"rcsui-config","version":9}`} {
		if _, err := Open([]byte(bad), pass); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if _, err := Seal(plain, "short"); err == nil {
		t.Error("short passphrase accepted")
	}
}
