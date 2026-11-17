//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package pager

import "syscall"

func (p *Pager) LockExclusive() error {
	return syscall.Flock(int(p.file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
