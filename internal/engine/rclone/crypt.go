package rclone

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
)

// ConfigCrypt règle le chiffrement de rclone.conf (mécanisme natif de rclone :
// NaCl secretbox, clé dérivée du mot de passe de configuration).
type ConfigCrypt struct {
	// Password : mot de passe de configuration (dérivé de la clé maître). Requis
	// pour chiffrer, et pour lire une configuration déjà chiffrée.
	Password string
	// Encrypt : état voulu. false sur une configuration chiffrée la déchiffre
	// (retour arrière).
	Encrypt bool
	// BackupDir reçoit une copie du fichier avant toute conversion.
	BackupDir string
}

// ConfigCryptResult décrit ce que Init a fait du fichier.
type ConfigCryptResult struct {
	Encrypted bool   // état du fichier après Init
	Converted bool   // le fichier vient d'être chiffré ou déchiffré
	Backup    string // copie faite avant conversion ("" si aucune)
}

// ErrConfigKey : la configuration est chiffrée avec une autre clé.
var ErrConfigKey = errors.New("rclone.conf is encrypted with another key (was RCSUI_SECRET_KEY or /data/secret.key changed?)")

// encryptedConfig indique si le contenu est une configuration rclone chiffrée
// (première ligne utile "RCLONE_ENCRYPT_V…", comme config.Decrypt).
func encryptedConfig(b []byte) bool {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, ";") || strings.HasPrefix(l, "#") {
			continue
		}
		return strings.HasPrefix(l, "RCLONE_ENCRYPT_V")
	}
	return false
}

// prepareConfigCrypt pose la clé avant le premier chargement de la
// configuration (librclone la charge à la première utilisation) et vérifie
// qu'elle ouvre un fichier déjà chiffré. Jamais d'invite interactive.
func prepareConfigCrypt(path string, c ConfigCrypt) (wasEncrypted bool, err error) {
	fs.GetConfig(context.Background()).AskPassword = false
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	wasEncrypted = encryptedConfig(data)
	if !wasEncrypted && !c.Encrypt {
		return false, nil
	}
	if c.Password == "" {
		return wasEncrypted, errors.New("rclone config password missing")
	}
	if err := config.SetConfigPassword(c.Password); err != nil {
		return wasEncrypted, err
	}
	if wasEncrypted {
		// Decrypt efface la clé si elle ne convient pas.
		if _, err := config.Decrypt(bytes.NewReader(data)); err != nil {
			config.ClearConfigPassword()
			return wasEncrypted, fmt.Errorf("%w: %v", ErrConfigKey, err)
		}
	}
	return wasEncrypted, nil
}

// finishConfigCrypt convertit le fichier si son état diffère de celui voulu,
// après une copie de sauvegarde. Appelé après librclone.Initialize.
func finishConfigCrypt(path string, c ConfigCrypt, wasEncrypted bool) (ConfigCryptResult, error) {
	res := ConfigCryptResult{Encrypted: wasEncrypted}
	if wasEncrypted == c.Encrypt {
		return res, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return res, err
	}
	if len(bytes.TrimSpace(data)) > 0 {
		if res.Backup, err = backupConfig(path, data, c.BackupDir, wasEncrypted); err != nil {
			return res, err
		}
	}
	// Charger avec la clé courante, puis l'ôter si l'on déchiffre.
	_ = config.FileSections()
	if !c.Encrypt {
		config.ClearConfigPassword()
	}
	if err := config.LoadedData().Save(); err != nil {
		return res, fmt.Errorf("save rclone config: %w", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		return res, err
	}
	if encryptedConfig(after) != c.Encrypt {
		return res, fmt.Errorf("rclone config: conversion did not apply (encrypted=%v)", !c.Encrypt)
	}
	res.Encrypted, res.Converted = c.Encrypt, true
	return res, nil
}

func backupConfig(path string, data []byte, dir string, encrypted bool) (string, error) {
	if dir == "" {
		dir = filepath.Dir(path)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	kind := "plain"
	if encrypted {
		kind = "encrypted"
	}
	name := fmt.Sprintf("rclone.conf.%s.%s.bak", time.Now().UTC().Format("20060102T150405Z"), kind)
	dst := filepath.Join(dir, name)
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return "", err
	}
	return dst, nil
}
