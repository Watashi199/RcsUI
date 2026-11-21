//go:build unix && !linux

package rsync

import "syscall"

func setPdeathsig(*syscall.SysProcAttr) {}
