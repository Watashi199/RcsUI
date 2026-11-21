//go:build linux

package rsync

import "syscall"

// setPdeathsig demande au noyau de tuer rsync si le serveur meurt. Le signal
// est lié au thread créateur : c'est un filet de sécurité, la récupération au
// boot (runner.Recover) reste la garantie principale.
func setPdeathsig(a *syscall.SysProcAttr) { a.Pdeathsig = syscall.SIGKILL }
