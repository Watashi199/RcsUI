// Package sshkeys gère les identités SSH (génération ed25519, import), les
// clés d'hôte approuvées (TOFU explicite) et fournit au moteur rsync la
// configuration SSH d'un run (clé déchiffrée temporaire, known_hosts).
package sshkeys

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"rcsui/internal/engine"
	"rcsui/internal/engine/rsync"
	"rcsui/internal/secrets"
	"rcsui/internal/store"
)

// Manager implémente rsync.SSHProvider.
type Manager struct {
	DB  *store.DB
	Box *secrets.Box
	// StateDir contient known_hosts (régénéré depuis la base).
	StateDir string
	// RunDir reçoit les clés déchiffrées le temps d'un run (supprimées ensuite).
	RunDir string
	// MountedDir est un ~/.ssh monté en lecture seule (vide : mode monté indisponible).
	MountedDir string

	mu sync.Mutex
}

var _ rsync.SSHProvider = (*Manager)(nil)

var (
	nameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$`)
	hostRe   = regexp.MustCompile(`^(?:[A-Za-z0-9][A-Za-z0-9.-]{0,252}|[0-9A-Fa-f:.]+)$`)
	unsafeRe = regexp.MustCompile(`[^A-Za-z0-9]`)
)

// KnownHostsFile retourne le chemin du fichier known_hosts géré.
func (m *Manager) KnownHostsFile() string { return filepath.Join(m.StateDir, "known_hosts") }

// ad lie le chiffré à l'identité : un chiffré ne peut pas être déplacé sur une autre ligne.
func ad(id int64) []byte { return []byte("ssh-identity:" + strconv.FormatInt(id, 10)) }

// PrivatePEM retourne la clé privée en clair (PEM OpenSSH) d'une identité.
// Réservé à l'export chiffré de la configuration.
func (m *Manager) PrivatePEM(ctx context.Context, id int64) ([]byte, error) {
	ident, err := m.DB.GetSSHIdentity(ctx, id)
	if err != nil {
		return nil, err
	}
	return m.Box.Open(ident.PrivateEnc, ad(ident.ID))
}

// Fingerprint retourne l'empreinte SHA256 d'une clé publique.
func Fingerprint(k ssh.PublicKey) string { return ssh.FingerprintSHA256(k) }

func (m *Manager) save(ctx context.Context, name string, priv any) (store.SSHIdentity, error) {
	if !nameRe.MatchString(name) {
		return store.SSHIdentity{}, fmt.Errorf("invalid identity name")
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return store.SSHIdentity{}, err
	}
	block, err := ssh.MarshalPrivateKey(priv, name)
	if err != nil {
		return store.SSHIdentity{}, err
	}
	pemBytes := pem.EncodeToMemory(block)
	pub := signer.PublicKey()
	ident := store.SSHIdentity{Name: name, KeyType: pub.Type(),
		PublicKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) + " rcsui-" + strings.ReplaceAll(name, " ", "_"),
		Fingerprint: Fingerprint(pub)}
	id, err := m.DB.CreateSSHIdentity(ctx, ident, func(id int64) ([]byte, error) { return m.Box.Seal(pemBytes, ad(id)) })
	if err != nil {
		return store.SSHIdentity{}, err
	}
	return m.DB.GetSSHIdentity(ctx, id)
}

// Generate crée une identité ed25519.
func (m *Manager) Generate(ctx context.Context, name string) (store.SSHIdentity, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return store.SSHIdentity{}, err
	}
	return m.save(ctx, name, priv)
}

// Import importe une clé privée PEM (OpenSSH, PKCS#1/8), éventuellement protégée.
// Elle est stockée sans passphrase mais chiffrée par la clé maître.
func (m *Manager) Import(ctx context.Context, name, pemData, passphrase string) (store.SSHIdentity, error) {
	if len(pemData) > 32*1024 {
		return store.SSHIdentity{}, fmt.Errorf("key too large")
	}
	var key any
	var err error
	if passphrase != "" {
		key, err = ssh.ParseRawPrivateKeyWithPassphrase([]byte(pemData), []byte(passphrase))
	} else {
		key, err = ssh.ParseRawPrivateKey([]byte(pemData))
	}
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		return store.SSHIdentity{}, fmt.Errorf("this key is protected: a passphrase is required")
	}
	if err != nil {
		return store.SSHIdentity{}, fmt.Errorf("cannot parse private key")
	}
	if k, ok := key.(*ed25519.PrivateKey); ok {
		key = *k
	}
	return m.save(ctx, name, key)
}

// ScannedKey est une clé d'hôte observée lors d'un scan.
type ScannedKey struct {
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
	Key         string `json:"key"` // format authorized_keys
}

// ValidHost vérifie un nom d'hôte ou une IP (sans crochets).
func ValidHost(h string) bool { return hostRe.MatchString(h) && !strings.HasPrefix(h, "-") }

// Scan récupère les clés d'hôte proposées par un serveur (une poignée de main
// par algorithme). Rien n'est enregistré : l'approbation est une étape explicite.
func Scan(ctx context.Context, host string, port int) ([]ScannedKey, error) {
	if !ValidHost(host) || port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid host or port")
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	var out []ScannedKey
	seen := map[string]bool{}
	var lastErr error
	for _, algo := range []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoRSASHA512} {
		var got ssh.PublicKey
		cfg := &ssh.ClientConfig{
			User:              "rcsui-scan",
			HostKeyAlgorithms: []string{algo},
			HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
				got = k
				return errors.New("scan only")
			},
			Timeout: 10 * time.Second,
		}
		d := net.Dialer{Timeout: 10 * time.Second}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("connect: %w", err)
		}
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		_, _, _, err = ssh.NewClientConn(conn, addr, cfg)
		conn.Close()
		if got == nil {
			lastErr = err
			continue
		}
		fp := Fingerprint(got)
		if seen[fp] {
			continue
		}
		seen[fp] = true
		out = append(out, ScannedKey{Type: got.Type(), Fingerprint: fp, Key: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(got)))})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no host key obtained: %v", lastErr)
	}
	return out, nil
}

// Approve enregistre une clé d'hôte (format authorized_keys) et régénère known_hosts.
func (m *Manager) Approve(ctx context.Context, host string, port int, key string, by *int64) (store.KnownHost, error) {
	if !ValidHost(host) || port < 1 || port > 65535 {
		return store.KnownHost{}, fmt.Errorf("invalid host or port")
	}
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil {
		return store.KnownHost{}, fmt.Errorf("invalid host key")
	}
	h := store.KnownHost{Host: strings.ToLower(host), Port: port, KeyType: pk.Type(),
		PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk))), Fingerprint: Fingerprint(pk), AddedBy: by}
	if _, err := m.DB.AddKnownHost(ctx, h); err != nil {
		return store.KnownHost{}, err
	}
	return h, m.WriteKnownHosts(ctx)
}

// Remove supprime une clé d'hôte et régénère known_hosts.
func (m *Manager) Remove(ctx context.Context, id int64) error {
	if err := m.DB.DeleteKnownHost(ctx, id); err != nil {
		return err
	}
	return m.WriteKnownHosts(ctx)
}

// WriteKnownHosts régénère known_hosts depuis la base (écriture atomique, 0600).
func (m *Manager) WriteKnownHosts(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	hosts, err := m.DB.ListKnownHosts(ctx)
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, h := range hosts {
		pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(h.PublicKey))
		if err != nil {
			continue
		}
		b.WriteString(knownhosts.Line([]string{knownhosts.Normalize(net.JoinHostPort(h.Host, strconv.Itoa(h.Port)))}, pk))
		b.WriteByte('\n')
	}
	if err := os.MkdirAll(m.StateDir, 0o700); err != nil {
		return err
	}
	tmp := m.KnownHostsFile() + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.KnownHostsFile())
}

func (m *Manager) hasKnownHost(ctx context.Context, host string, port int) (bool, error) {
	hosts, err := m.DB.ListKnownHosts(ctx)
	if err != nil {
		return false, err
	}
	for _, h := range hosts {
		if strings.EqualFold(h.Host, host) && h.Port == port {
			return true, nil
		}
	}
	return false, nil
}

// MountedAvailable indique si le mode ~/.ssh monté est utilisable.
func (m *Manager) MountedAvailable() bool {
	if m.MountedDir == "" || filepath.Base(m.MountedDir) != ".ssh" {
		return false
	}
	st, err := os.Stat(m.MountedDir)
	return err == nil && st.IsDir()
}

// Check implémente rsync.SSHProvider.
func (m *Manager) Check(ctx context.Context, t *engine.SSHTarget) error {
	if t.Identity.Mounted {
		if !m.MountedAvailable() {
			return fmt.Errorf("mounted ~/.ssh is not configured (RCSUI_SSH_DIR must point to a directory named .ssh)")
		}
		return nil // clés et known_hosts : ceux du ~/.ssh monté
	}
	if t.Identity.ManagedID == nil {
		return fmt.Errorf("no identity selected")
	}
	if _, err := m.DB.GetSSHIdentity(ctx, *t.Identity.ManagedID); err != nil {
		return fmt.Errorf("unknown ssh identity")
	}
	ok, err := m.hasKnownHost(ctx, t.Host, t.Port)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("host %s:%d is not in known hosts: scan and approve its key first", t.Host, t.Port)
	}
	return nil
}

// TestConnection exécute `ssh … user@host true` avec exactement les options
// qu'utilisera rsync. Retourne la sortie (erreurs ssh) pour le diagnostic.
func (m *Manager) TestConnection(ctx context.Context, t *engine.SSHTarget) (string, error) {
	if err := rsync.ValidateSSHTarget(t); err != nil {
		return "", err
	}
	cfg, env, cleanup, err := m.Prepare(ctx, "test"+strconv.FormatInt(time.Now().UnixNano(), 10), t)
	if err != nil {
		return "", err
	}
	defer cleanup()
	e, err := rsync.SSHCommand(t, cfg)
	if err != nil {
		return "", err
	}
	parts := strings.Fields(e)
	args := append(parts[1:], "--", t.User+"@"+t.Host, "true")
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, parts[0], args...)
	cmd.Env = append([]string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + m.StateDir, "LC_ALL=C.UTF-8"}, env...)
	out, err := cmd.CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	if err != nil {
		if msg == "" {
			msg = err.Error()
		}
		return msg, fmt.Errorf("ssh connection failed")
	}
	return msg, nil
}

// SSHBinaryAvailable indique si le client ssh est installé.
func SSHBinaryAvailable() bool {
	_, err := exec.LookPath("ssh")
	return err == nil
}

// Prepare implémente rsync.SSHProvider : déchiffre la clé dans RunDir (0600).
func (m *Manager) Prepare(ctx context.Context, runID string, t *engine.SSHTarget) (*rsync.SSHConfig, []string, func(), error) {
	if err := m.Check(ctx, t); err != nil {
		return nil, nil, nil, err
	}
	if t.Identity.Mounted {
		// HOME = parent du ~/.ssh monté : ssh y trouve config, clés et known_hosts.
		return &rsync.SSHConfig{Mounted: true}, []string{"HOME=" + filepath.Dir(m.MountedDir)}, func() { /* rien à nettoyer */ }, nil
	}
	ident, err := m.DB.GetSSHIdentity(ctx, *t.Identity.ManagedID)
	if err != nil {
		return nil, nil, nil, err
	}
	pemBytes, err := m.Box.Open(ident.PrivateEnc, ad(ident.ID))
	if err != nil {
		return nil, nil, nil, err
	}
	if err := os.MkdirAll(m.RunDir, 0o700); err != nil {
		return nil, nil, nil, err
	}
	keyFile := filepath.Join(m.RunDir, unsafeRe.ReplaceAllString(runID, "")+".key")
	if err := os.WriteFile(keyFile, pemBytes, 0o600); err != nil {
		return nil, nil, nil, err
	}
	cleanup := func() { _ = os.Remove(keyFile) }
	return &rsync.SSHConfig{KeyFile: filepath.ToSlash(keyFile), KnownHostsFile: filepath.ToSlash(m.KnownHostsFile())}, nil, cleanup, nil
}
