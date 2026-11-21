package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"rcsui/internal/secrets"
	"rcsui/internal/sshkeys"
	"rcsui/internal/store"
)

func sshServer(t *testing.T) (string, int) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _, _, _ = ssh.NewServerConn(c, cfg) }()
		}
	}()
	return "127.0.0.1", l.Addr().(*net.TCPAddr).Port
}

// withSSH reconstruit le serveur de test avec un gestionnaire SSH.
func withSSH(t *testing.T, h *harness) {
	t.Helper()
	box, _ := secrets.New(bytes.Repeat([]byte{3}, 32))
	dir := t.TempDir()
	mgr := &sshkeys.Manager{DB: h.db, Box: box, StateDir: filepath.Join(dir, "ssh"), RunDir: filepath.Join(dir, "run")}
	h.server.SSH = mgr
	h.server.Info.SSHAvailable = true
	h.ts.Close()
	h.ts = httptest.NewServer(h.server.Handler())
	t.Cleanup(h.ts.Close)
}

func TestSSHEndpoints(t *testing.T) {
	h := newHarness(t)
	withSSH(t, h)
	admin := setupAdmin(t, h)

	code, out := admin.do("POST", "/api/v1/ssh/identities", map[string]any{"name": "nas", "generate": true})
	if code != 201 || !strings.HasPrefix(out["publicKey"].(string), "ssh-ed25519 ") {
		t.Fatalf("generate: %d %v", code, out)
	}
	if strings.Contains(mustJSON(out), "PRIVATE") || out["privateEnc"] != nil {
		t.Fatalf("private key leaked: %v", out)
	}
	idID := int64(out["id"].(float64))
	if code, _ := admin.do("POST", "/api/v1/ssh/identities", map[string]any{"name": "nas", "generate": true}); code != 409 {
		t.Errorf("duplicate identity: %d", code)
	}
	if code, _ := admin.do("POST", "/api/v1/ssh/identities", map[string]any{"name": "x"}); code != 400 {
		t.Errorf("empty identity request: %d", code)
	}

	host, port := sshServer(t)
	code, out = admin.do("POST", "/api/v1/ssh/known-hosts/scan", map[string]any{"host": host, "port": port})
	keys, _ := out["keys"].([]any)
	if code != 200 || len(keys) != 1 {
		t.Fatalf("scan: %d %v", code, out)
	}
	key := keys[0].(map[string]any)["key"].(string)
	if code, out := admin.do("POST", "/api/v1/ssh/known-hosts", map[string]any{"host": host, "port": port, "key": key}); code != 201 {
		t.Fatalf("approve: %d %v", code, out)
	}
	if code, _ := admin.do("POST", "/api/v1/ssh/known-hosts", map[string]any{"host": host, "port": port, "key": "garbage"}); code != 400 {
		t.Errorf("garbage host key accepted: %d", code)
	}

	op, opID := addUser(t, h, admin, "op", store.RoleOperator)
	if _, out := op.do("GET", "/api/v1/ssh/identities", nil); len(items(out)) != 0 {
		t.Errorf("op sees ungranted identity: %v", out)
	}
	if code, _ := op.do("POST", "/api/v1/ssh/known-hosts/scan", map[string]any{"host": host, "port": port}); code != 403 {
		t.Errorf("op scans: %d", code)
	}
	job := map[string]any{"name": "ssh-job", "engine": "rsync", "mode": "copy",
		"source": map[string]any{"type": "local", "path": filepath.Join(h.root, "a")},
		"destination": map[string]any{"type": "ssh", "path": "/backup", "ssh": map[string]any{
			"host": host, "port": port, "user": "backup", "identity": map[string]any{"managed": idID}}}}
	if code, out := op.do("POST", "/api/v1/jobs", job); code != 400 || !strings.Contains(mustJSON(out), "ssh identity") {
		t.Errorf("op uses ungranted identity: %d %v", code, out)
	}
	if code, _ := op.do("POST", "/api/v1/ssh/test", map[string]any{"host": host, "port": port, "user": "backup", "identity": map[string]any{"mounted": true}}); code != 403 {
		t.Errorf("op uses mounted ~/.ssh: %d", code)
	}
	if code, out := admin.do("PUT", "/api/v1/ssh/identities/"+itoa(idID)+"/grants", map[string]any{"userIds": []int64{opID}}); code != 200 {
		t.Fatalf("grant identity: %d %v", code, out)
	}
	if _, out := op.do("GET", "/api/v1/ssh/identities", nil); len(items(out)) != 1 {
		t.Errorf("op does not see granted identity: %v", out)
	}
	if code, out := op.do("POST", "/api/v1/jobs", job); code != 201 {
		t.Errorf("op creates ssh job: %d %v", code, out)
	}
	// Identité utilisée par un job : suppression refusée.
	if code, _ := admin.do("DELETE", "/api/v1/ssh/identities/"+itoa(idID), nil); code != 409 {
		t.Errorf("delete identity in use: %d", code)
	}
}
