package rclone

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// librclone ne s'initialise qu'une fois par processus : chaque démarrage du
// serveur est simulé par un sous-processus de test (TestCryptChild).
type cryptChildOut struct {
	Crypt   ConfigCryptResult
	Remotes []string
	Err     string
	KeyErr  bool
}

func TestCryptChild(t *testing.T) {
	conf := os.Getenv("RCSUI_CRYPT_CHILD")
	if conf == "" {
		t.Skip("helper process")
	}
	var out cryptChildOut
	c, err := Init(conf, filepath.Join(filepath.Dir(conf), "cache"), ConfigCrypt{
		Password: os.Getenv("RCSUI_CRYPT_PASS"), Encrypt: os.Getenv("RCSUI_CRYPT_ENCRYPT") == "1",
		BackupDir: filepath.Join(filepath.Dir(conf), "backups")})
	if err != nil {
		out.Err, out.KeyErr = err.Error(), errors.Is(err, ErrConfigKey)
	} else {
		out.Crypt = c.Crypt
		if name := os.Getenv("RCSUI_CRYPT_CREATE"); name != "" {
			if _, err := c.CreateRemote(name, "memory", map[string]any{}, RemoteValidator{}); err != nil {
				out.Err = err.Error()
			}
		}
		list, err := c.ListRemotes()
		if err != nil {
			out.Err = err.Error()
		}
		for _, r := range list {
			out.Remotes = append(out.Remotes, r.Name)
		}
	}
	b, _ := json.Marshal(out)
	os.Stdout.Write(append([]byte("\nCRYPT-RESULT "), append(b, '\n')...))
}

func startServer(t *testing.T, conf, pass string, encrypt bool, create string) cryptChildOut {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCryptChild$", "-test.count=1")
	enc := "0"
	if encrypt {
		enc = "1"
	}
	cmd.Env = append(os.Environ(), "RCSUI_CRYPT_CHILD="+conf, "RCSUI_CRYPT_PASS="+pass,
		"RCSUI_CRYPT_ENCRYPT="+enc, "RCSUI_CRYPT_CREATE="+create, "RCLONE_CONFIG_PASS=")
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v\n%s", err, b)
	}
	_, res, ok := strings.Cut(string(b), "CRYPT-RESULT ")
	if !ok {
		t.Fatalf("no result:\n%s", b)
	}
	var out cryptChildOut
	if err := json.Unmarshal([]byte(strings.SplitN(res, "\n", 2)[0]), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func fileState(t *testing.T, p string) (encrypted bool, content string) {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return encryptedConfig(b), string(b)
}

func TestConfigEncryptionLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	dir := t.TempDir()
	conf := filepath.Join(dir, "rclone.conf")
	if err := os.WriteFile(conf, []byte("[legacy]\ntype = memory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backups := func() []string {
		m, _ := filepath.Glob(filepath.Join(dir, "backups", "rclone.conf.*.bak"))
		return m
	}

	// 1. Configuration existante en clair : chiffrée au démarrage, copie conservée.
	out := startServer(t, conf, "key-A", true, "")
	if out.Err != "" || !out.Crypt.Converted || !out.Crypt.Encrypted || len(out.Remotes) != 1 {
		t.Fatalf("migration: %+v", out)
	}
	if enc, content := fileState(t, conf); !enc || strings.Contains(content, "legacy") {
		t.Fatalf("file not encrypted:\n%s", content)
	}
	if b := backups(); len(b) != 1 || !strings.HasSuffix(b[0], ".plain.bak") {
		t.Fatalf("backups: %v", b)
	} else if raw, _ := os.ReadFile(b[0]); !strings.Contains(string(raw), "[legacy]") {
		t.Fatalf("backup content: %s", raw)
	}

	// 2. Redémarrage : lecture sans invite ; une écriture reste chiffrée.
	out = startServer(t, conf, "key-A", true, "added")
	if out.Err != "" || out.Crypt.Converted || !out.Crypt.Encrypted || strings.Join(out.Remotes, ",") != "added,legacy" {
		t.Fatalf("restart: %+v", out)
	}
	if enc, _ := fileState(t, conf); !enc {
		t.Fatal("write after restart stored in clear")
	}

	// 3. Autre clé maître : refus explicite, fichier intact.
	_, before := fileState(t, conf)
	out = startServer(t, conf, "key-B", true, "")
	if !out.KeyErr {
		t.Fatalf("wrong key accepted: %+v", out)
	}
	if _, after := fileState(t, conf); after != before {
		t.Fatal("file modified by a failed start")
	}

	// 4. Retour arrière (RCSUI_RCLONE_CONFIG_ENCRYPT=false) : déchiffrée, copie chiffrée conservée.
	out = startServer(t, conf, "key-A", false, "")
	if out.Err != "" || !out.Crypt.Converted || out.Crypt.Encrypted || len(out.Remotes) != 2 {
		t.Fatalf("decrypt: %+v", out)
	}
	if enc, content := fileState(t, conf); enc || !strings.Contains(content, "[added]") {
		t.Fatalf("file not decrypted:\n%s", content)
	}
	if b := backups(); len(b) != 2 {
		t.Fatalf("backups: %v", b)
	}

	// 5. En clair sans chiffrement voulu : rien ne change.
	out = startServer(t, conf, "", false, "")
	if out.Err != "" || out.Crypt.Converted || out.Crypt.Encrypted || len(out.Remotes) != 2 {
		t.Fatalf("plain: %+v", out)
	}
}

// Installation neuve : le fichier vide est chiffré dès le premier démarrage.
func TestConfigEncryptionFreshInstall(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	conf := filepath.Join(t.TempDir(), "sub", "rclone.conf")
	out := startServer(t, conf, "key-A", true, "first")
	if out.Err != "" || !out.Crypt.Encrypted || out.Crypt.Backup != "" || strings.Join(out.Remotes, ",") != "first" {
		t.Fatalf("fresh install: %+v", out)
	}
	if enc, content := fileState(t, conf); !enc {
		t.Fatalf("fresh config not encrypted:\n%s", content)
	}
}

func TestEncryptedConfigDetection(t *testing.T) {
	for in, want := range map[string]bool{
		"":                                     false,
		"[r]\ntype = memory\n":                 false,
		"# comment\n\nRCLONE_ENCRYPT_V0:\nabc": true,
		"; c\nRCLONE_ENCRYPT_V1:\n":            true,
	} {
		if got := encryptedConfig([]byte(in)); got != want {
			t.Errorf("encryptedConfig(%q) = %v", in, got)
		}
	}
}
