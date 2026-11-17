//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package raftstore

import (
	"errors"
	"os"
)

func lock(_ *os.File) error {
	return errors.New("raft store locking is not implemented for this platform")
}
