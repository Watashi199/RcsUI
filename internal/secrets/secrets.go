// Package secrets chiffre les données sensibles au repos (clés SSH privées)
// avec AES-256-GCM et une clé maître (RCSUI_SECRET_KEY ou /data/secret.key).
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Box chiffre et déchiffre avec une clé de 32 octets.
type Box struct{ aead cipher.AEAD }

// New crée une Box depuis une clé de 32 octets.
func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secret key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal chiffre ; résultat : nonce || ciphertext. ad lie le chiffré à son
// contexte (ex. "ssh-identity:4") pour empêcher l'échange de deux valeurs.
func (b *Box) Seal(plain, ad []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plain, ad), nil
}

// ErrDecrypt : clé maître différente ou donnée altérée.
var ErrDecrypt = errors.New("cannot decrypt secret (wrong RCSUI_SECRET_KEY or corrupted data)")

// Open déchiffre une valeur produite par Seal.
func (b *Box) Open(sealed, ad []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n+b.aead.Overhead() {
		return nil, ErrDecrypt
	}
	out, err := b.aead.Open(nil, sealed[:n], sealed[n:], ad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return out, nil
}

// LoadOrCreate retourne la clé maître : valeur fournie (base64), sinon contenu
// du fichier path, sinon une nouvelle clé aléatoire écrite dans path (0600).
func LoadOrCreate(provided, path string) ([]byte, error) {
	if provided != "" {
		return decodeKey(provided)
	}
	if b, err := os.ReadFile(path); err == nil {
		return decodeKey(string(b))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n"); err != nil {
		return nil, err
	}
	return key, nil
}

// Derive dérive de la clé maître une sous-clé de 32 octets propre à un usage
// (HKDF-SHA256) : une seule clé à sauvegarder, des clés distinctes par usage.
func Derive(master []byte, purpose string) ([]byte, error) {
	return hkdf.Key(sha256.New, master, nil, purpose, 32)
}

// RcloneConfigPassword est le mot de passe de configuration rclone (chiffrement
// de rclone.conf), dérivé de la clé maître.
func RcloneConfigPassword(master []byte) (string, error) {
	k, err := Derive(master, "rcsui/rclone-config/v1")
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(k), nil
}

func decodeKey(s string) ([]byte, error) {
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("secret key must be base64: %w", err)
	}
	if len(k) != 32 {
		return nil, fmt.Errorf("secret key must decode to 32 bytes, got %d", len(k))
	}
	return k, nil
}
