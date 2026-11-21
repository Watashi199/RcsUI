package sshkeys

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"rcsui/internal/engine"
	"rcsui/internal/secrets"
	"rcsui/internal/store"
)

func newManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(context.Background(), filepath.Join(dir, "t.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	box, _ := secrets.New(bytes.Repeat([]byte{1}, 32))
	return &Manager{DB: db, Box: box, StateDir: filepath.Join(dir, "ssh"), RunDir: filepath.Join(dir, "run")}
}

// startSSHServer lance un serveur SSH minimal présentant deux clés d'hôte.
func startSSHServer(t *testing.T) (host string, port int, keys []ssh.PublicKey) {
	t.Helper()
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	_, edPriv, _ := ed25519.GenerateKey(rand.Reader)
	ecPriv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	for _, k := range []any{edPriv, ecPriv} {
		s, err := ssh.NewSignerFromKey(k)
		if err != nil {
			t.Fatal(err)
		}
		cfg.AddHostKey(s)
		keys = append(keys, s.PublicKey())
	}
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
			go func() {
				defer c.Close()
				_, _, _, _ = ssh.NewServerConn(c, cfg)
			}()
		}
	}()
	return "127.0.0.1", l.Addr().(*net.TCPAddr).Port, keys
}

func TestGenerateAndPrepare(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	id, err := m.Generate(ctx, "nas backup")
	if err != nil {
		t.Fatal(err)
	}
	if id.KeyType != "ssh-ed25519" || !strings.HasPrefix(id.PublicKey, "ssh-ed25519 ") || !strings.HasPrefix(id.Fingerprint, "SHA256:") {
		t.Errorf("identity %+v", id)
	}
	if bytes.Contains(id.PrivateEnc, []byte("PRIVATE KEY")) {
		t.Fatal("private key stored in clear")
	}
	if _, err := m.Generate(ctx, "-bad name"); err == nil {
		t.Error("invalid name accepted")
	}
	target := &engine.SSHTarget{Host: "127.0.0.1", Port: 2222, User: "u", Identity: engine.SSHIdentity{ManagedID: &id.ID}}
	if err := m.Check(ctx, target); err == nil || !strings.Contains(err.Error(), "known hosts") {
		t.Errorf("unknown host accepted: %v", err)
	}
	_, hostKey, _ := ed25519.GenerateKey(rand.Reader)
	hs, _ := ssh.NewSignerFromKey(hostKey)
	if _, err := m.Approve(ctx, "127.0.0.1", 2222, string(ssh.MarshalAuthorizedKey(hs.PublicKey())), nil); err != nil {
		t.Fatal(err)
	}
	cfg, env, cleanup, err := m.Prepare(ctx, "01RUN", target)
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 0 || cfg.Mounted {
		t.Errorf("cfg=%+v env=%v", cfg, env)
	}
	b, err := os.ReadFile(cfg.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ssh.ParsePrivateKey(b); err != nil {
		t.Errorf("decrypted key unusable: %v", err)
	}
	if st, _ := os.Stat(cfg.KeyFile); os.PathSeparator == '/' && st.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v", st.Mode())
	}
	cleanup()
	if _, err := os.Stat(cfg.KeyFile); !os.IsNotExist(err) {
		t.Error("key not removed after run")
	}
	bad := int64(999)
	target.Identity.ManagedID = &bad
	if err := m.Check(ctx, target); err == nil {
		t.Error("unknown identity accepted")
	}
	target.Identity = engine.SSHIdentity{Mounted: true}
	if err := m.Check(ctx, target); err == nil {
		t.Error("mounted mode accepted without RCSUI_SSH_DIR")
	}
	m.MountedDir = filepath.Join(t.TempDir(), ".ssh")
	os.MkdirAll(m.MountedDir, 0o700)
	_, env, _, err = m.Prepare(ctx, "01RUN", target)
	if err != nil || len(env) != 1 || env[0] != "HOME="+filepath.Dir(m.MountedDir) {
		t.Errorf("mounted prepare: %v %v", env, err)
	}
}

func TestImport(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKeyWithPassphrase(priv, "c", []byte("secret-pass"))
	pemData := string(pem.EncodeToMemory(block))
	if _, err := m.Import(ctx, "imp", pemData, ""); err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Errorf("protected key without passphrase: %v", err)
	}
	if _, err := m.Import(ctx, "imp", pemData, "wrong"); err == nil {
		t.Error("wrong passphrase accepted")
	}
	id, err := m.Import(ctx, "imp", pemData, "secret-pass")
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := ssh.NewSignerFromKey(priv)
	if id.Fingerprint != Fingerprint(signer.PublicKey()) {
		t.Error("fingerprint mismatch after import")
	}
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	b2, _ := ssh.MarshalPrivateKey(ec, "")
	if _, err := m.Import(ctx, "ec", string(pem.EncodeToMemory(b2)), ""); err != nil {
		t.Errorf("ecdsa import: %v", err)
	}
	if _, err := m.Import(ctx, "garbage", "not a key", ""); err == nil {
		t.Error("garbage accepted")
	}
	if _, err := m.Import(ctx, "imp", pemData, "secret-pass"); err == nil {
		t.Error("duplicate name accepted")
	}
}

func TestScanAndKnownHostsFile(t *testing.T) {
	host, port, keys := startSSHServer(t)
	got, err := Scan(context.Background(), host, port)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("scanned %d keys: %+v", len(got), got)
	}
	m := newManager(t)
	for _, k := range got {
		if _, err := m.Approve(context.Background(), host, port, k.Key, nil); err != nil {
			t.Fatal(err)
		}
	}
	cb, err := knownhosts.New(m.KnownHostsFile())
	if err != nil {
		t.Fatal(err)
	}
	addr := &net.TCPAddr{IP: net.ParseIP(host), Port: port}
	hp := net.JoinHostPort(host, strconv.Itoa(port))
	for _, k := range keys {
		if err := cb(hp, addr, k); err != nil {
			t.Errorf("known_hosts rejects approved key %s: %v", k.Type(), err)
		}
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := ssh.NewSignerFromKey(other)
	if err := cb(hp, addr, otherSigner.PublicKey()); err == nil {
		t.Error("known_hosts accepts an unknown key (MITM)")
	}
	if _, err := Scan(context.Background(), "-oProxyCommand=x", 22); err == nil {
		t.Error("invalid host accepted")
	}
	list, _ := m.DB.ListKnownHosts(context.Background())
	if err := m.Remove(context.Background(), list[0].ID); err != nil {
		t.Fatal(err)
	}
	if l2, _ := m.DB.ListKnownHosts(context.Background()); len(l2) != 1 {
		t.Errorf("remove: %d left", len(l2))
	}
}
