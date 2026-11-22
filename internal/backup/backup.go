// Package backup chiffre l'export de configuration (M11) avec une phrase de
// passe : clé dérivée par argon2id, chiffrement AES-256-GCM. Le fichier est
// autonome : il ne dépend pas de la clé maître de l'instance d'origine, ce qui
// permet de l'importer sur une autre machine.
package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Format identifie le fichier.
const Format = "rcsui-config"

// Version du format d'enveloppe.
const Version = 1

// MinPassphrase est la longueur minimale de la phrase de passe (caractères).
const MinPassphrase = 12

// KDF décrit la dérivation de clé (paramètres stockés dans le fichier).
type KDF struct {
	Name      string `json:"name"` // "argon2id"
	Salt      []byte `json:"salt"`
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memoryKiB"`
	Threads   uint8  `json:"threads"`
}

// Envelope est le contenu du fichier exporté.
type Envelope struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	KDF     KDF    `json:"kdf"`
	Nonce   []byte `json:"nonce"`
	Data    []byte `json:"data"` // JSON chiffré
}

// Paramètres par défaut (recommandation RFC 9106, profil mémoire réduite).
var defaultKDF = KDF{Name: "argon2id", Time: 3, MemoryKiB: 64 * 1024, Threads: 4}

// Bornes acceptées à l'import : un fichier forgé ne doit pas épuiser la machine.
const (
	maxTime      = 10
	maxMemoryKiB = 256 * 1024
	maxThreads   = 16
)

// ErrPassphrase : phrase de passe incorrecte ou fichier altéré.
var ErrPassphrase = errors.New("wrong passphrase or corrupted file")

// CheckPassphrase vérifie la longueur minimale.
func CheckPassphrase(p string) error {
	if utf8.RuneCountInString(p) < MinPassphrase {
		return fmt.Errorf("passphrase must be at least %d characters", MinPassphrase)
	}
	if len(p) > 1024 {
		return errors.New("passphrase too long")
	}
	return nil
}

func aead(pass string, k KDF) (cipher.AEAD, error) {
	key := argon2.IDKey([]byte(pass), k.Salt, k.Time, k.MemoryKiB, k.Threads, 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// header lie le chiffré à son format et à ses paramètres de dérivation.
func header(k KDF) []byte {
	b, _ := json.Marshal(struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
		KDF     KDF    `json:"kdf"`
	}{Format, Version, k})
	return b
}

// Seal chiffre plain avec la phrase de passe et retourne le fichier (JSON).
func Seal(plain []byte, pass string) ([]byte, error) {
	if err := CheckPassphrase(pass); err != nil {
		return nil, err
	}
	k := defaultKDF
	k.Salt = make([]byte, 16)
	if _, err := rand.Read(k.Salt); err != nil {
		return nil, err
	}
	a, err := aead(pass, k)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	env := Envelope{Format: Format, Version: Version, KDF: k, Nonce: nonce, Data: a.Seal(nil, nonce, plain, header(k))}
	return json.MarshalIndent(env, "", "  ")
}

// Open déchiffre un fichier produit par Seal.
func Open(file []byte, pass string) ([]byte, error) {
	var env Envelope
	if err := json.Unmarshal(file, &env); err != nil {
		return nil, errors.New("not an RcsUI configuration export")
	}
	if env.Format != Format {
		return nil, errors.New("not an RcsUI configuration export")
	}
	if env.Version != Version {
		return nil, fmt.Errorf("unsupported export version %d", env.Version)
	}
	k := env.KDF
	if k.Name != "argon2id" || len(k.Salt) < 16 || k.Time < 1 || k.Time > maxTime ||
		k.MemoryKiB < 8*1024 || k.MemoryKiB > maxMemoryKiB || k.Threads < 1 || k.Threads > maxThreads {
		return nil, errors.New("unsupported key derivation parameters")
	}
	a, err := aead(pass, k)
	if err != nil {
		return nil, err
	}
	if len(env.Nonce) != a.NonceSize() {
		return nil, ErrPassphrase
	}
	plain, err := a.Open(nil, env.Nonce, env.Data, header(k))
	if err != nil {
		return nil, ErrPassphrase
	}
	return plain, nil
}
