//go:build unix

package rsync

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcAttrs place rsync dans son propre groupe de processus pour pouvoir
// signaler rsync, ses forks et ssh d'un coup.
func setProcAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	setPdeathsig(cmd.SysProcAttr)
}

func terminateGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	return syscall.Kill(-p.Pid, syscall.SIGTERM)
}

func killGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	return syscall.Kill(-p.Pid, syscall.SIGKILL)
}

// KillOrphanGroup tue un groupe de processus orphelin (récupération au boot).
func KillOrphanGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}
