//go:build linux

package runner

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"strconv"
	"syscall"
)

// killOrphan tue le groupe de processus pid uniquement s'il appartient bien
// au run (variable RCSUI_RUN_ID posée au lancement) : un pid peut avoir été
// réutilisé par un autre processus depuis.
func killOrphan(pid int, runID string) (bool, error) {
	env, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	marker := []byte("RCSUI_RUN_ID=" + runID)
	for _, kv := range bytes.Split(env, []byte{0}) {
		if bytes.Equal(kv, marker) {
			return true, syscall.Kill(-pid, syscall.SIGKILL)
		}
	}
	return false, nil
}
