package replication

import "errors"

var (
	ErrInvalidConfig   = errors.New("invalid replication configuration")
	ErrStateMismatch   = errors.New("database position does not match the Raft journal")
	ErrInvalidProposal = errors.New("invalid replicated command envelope")
	ErrInvalidMessage  = errors.New("invalid or unsupported peer message")
	ErrInvalidEntry    = errors.New("invalid or unsupported replicated entry")
	ErrInvalidRead     = errors.New("invalid read barrier context")
	ErrNotLeader       = errors.New("replication node is not the leader")
	ErrStopped         = errors.New("replication node stopped after a processing failure")
	ErrClosed          = errors.New("replication node is closed")
)
