package raftstore

import "errors"

var (
	ErrClosed              = errors.New("raft store is closed")
	ErrFailed              = errors.New("raft store stopped after a storage failure")
	ErrLocked              = errors.New("cannot acquire exclusive raft store lock")
	ErrIdentity            = errors.New("raft store group or node identity mismatch")
	ErrCorrupt             = errors.New("invalid raft store contents")
	ErrInvalidUpdate       = errors.New("invalid raft storage update")
	ErrBatchTooLarge       = errors.New("raft storage batch exceeds the size limit")
	ErrSnapshotUnsupported = errors.New("raft snapshot installation is not implemented")
	ErrAsyncUnsupported    = errors.New("raft asynchronous storage messages are not supported by Save")
)
