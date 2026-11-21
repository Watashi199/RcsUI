// Package auth gère les mots de passe (argon2id), les sessions par cookie,
// la protection CSRF et la limitation des tentatives de connexion.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Paramètres argon2id (PLAN.md §8 : m=64 MiB, t=3, p=2).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
	saltLen      = 16
	// MinPasswordLen est la longueur minimale d'un mot de passe.
	MinPasswordLen = 10
)

// hashSem borne le nombre de hachages simultanés (64 MiB chacun) : anti-DoS mémoire.
var hashSem = make(chan struct{}, 2)

// ErrMismatch est retournée quand le mot de passe ne correspond pas.
var ErrMismatch = errors.New("password mismatch")

// dummyHash sert à égaliser le temps de réponse quand le compte n'existe pas.
var dummyHash = mustHash("rcsui-dummy-password-for-timing")

func mustHash(pw string) string {
	h, err := HashPassword(pw)
	if err != nil {
		panic(err)
	}
	return h
}

// CheckPasswordPolicy vérifie la politique de mot de passe.
func CheckPasswordPolicy(pw string) error {
	if utf8.RuneCountInString(pw) < MinPasswordLen {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLen)
	}
	if len(pw) > 1024 {
		return fmt.Errorf("password too long")
	}
	return nil
}

// HashPassword retourne un hash PHC : $argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hashSem <- struct{}{}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	<-hashSem
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// VerifyPassword compare pw au hash PHC en temps constant. Un hash vide
// (compte sans mot de passe local) est toujours refusé, après un calcul
// factice pour ne pas révéler l'existence du compte par le temps de réponse.
func VerifyPassword(pw, phc string) error {
	if phc == "" {
		_ = verify(pw, dummyHash)
		return ErrMismatch
	}
	return verify(pw, phc)
}

// DummyVerify consomme le même temps qu'une vérification réelle.
func DummyVerify(pw string) { _ = verify(pw, dummyHash) }

func verify(pw, phc string) error {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return fmt.Errorf("unsupported hash format")
	}
	var v int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return fmt.Errorf("unsupported argon2 version")
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return fmt.Errorf("bad argon2 params")
	}
	if m > 1<<20 || t > 10 || p > 16 || m == 0 || t == 0 || p == 0 {
		return fmt.Errorf("argon2 params out of range")
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return err
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return fmt.Errorf("bad argon2 hash")
	}
	hashSem <- struct{}{}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	<-hashSem
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}

// RandomToken retourne n octets aléatoires encodés en base64url.
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// RandomPassword génère un mot de passe lisible (alphabet sans caractères
// ambigus ; le biais du modulo est négligeable : 256 % 57 = 28).
func RandomPassword(n int) string {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	out := make([]byte, 0, n)
	buf := make([]byte, 1)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			panic(err)
		}
		if int(buf[0]) >= 256-256%len(alphabet) { // rejet : pas de biais
			continue
		}
		out = append(out, alphabet[int(buf[0])%len(alphabet)])
	}
	return string(out)
}
