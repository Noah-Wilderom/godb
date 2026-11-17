//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package pager

import "errors"

func (p *Pager) LockExclusive() error {
	return errors.New("database locking is not implemented for this platform")
}
