//go:build !unix

package rsync

import (
	"os"
	"os/exec"
)

// Plateformes non Unix (développement sous Windows) : pas de groupe de
// processus ; on se contente de tuer le processus principal.
func setProcAttrs(cmd *exec.Cmd) { /* pas de process group hors Unix */ }

func terminateGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	return p.Kill()
}

func killGroup(p *os.Process) error { return terminateGroup(p) }

// KillOrphanGroup tue un processus orphelin (récupération au boot).
func KillOrphanGroup(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
