package rclone

// Imports à effet de bord : enregistrement des backends et des appels rc.
// Dérivés du shim officiel librclone/librclone.go, sans le support mount
// (FUSE inutile et lourd). Binaire complet mesuré au spike M0 : ~76 Mo
// (linux/amd64, -s -w) ; ~16 Mo avec seulement local+memory.
import (
	_ "github.com/rclone/rclone/backend/all"   // tous les backends
	_ "github.com/rclone/rclone/fs/operations" // operations/*
	_ "github.com/rclone/rclone/fs/sync"       // sync/*
)
