package raftstore

import (
	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"
)

func (s *Store) InitialState() (*raftpb.HardState, *raftpb.ConfState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if err := s.check(); err != nil {
		return nil, nil, err
	}

	return proto.Clone(s.hard).(*raftpb.HardState), &raftpb.ConfState{}, nil
}

func (s *Store) Entries(lo, hi, maxSize uint64) ([]*raftpb.Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if err := s.check(); err != nil {
		return nil, err
	}

	if lo == 0 {
		return nil, raft.ErrCompacted
	}

	if hi < lo || hi > uint64(len(s.entries))+1 {
		return nil, raft.ErrUnavailable
	}

	var (
		result []*raftpb.Entry
		size   uint64
	)

	for _, entry := range s.entries[lo-1 : hi-1] {
		n := uint64(proto.Size(entry)) // #nosec G115 -- Protobuf Size is nonnegative; stored entries are bounded by MaxBatchBytes.
		if len(result) != 0 && (size >= maxSize || n > maxSize-size) {
			break
		}

		result = append(result, proto.Clone(entry).(*raftpb.Entry))
		size += n
	}

	return result, nil
}

func (s *Store) Term(index uint64) (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if err := s.check(); err != nil {
		return 0, err
	}

	if index == 0 {
		return 0, nil
	}

	if index > uint64(len(s.entries)) {
		return 0, raft.ErrUnavailable
	}

	return s.entries[index-1].GetTerm(), nil
}

func (s *Store) FirstIndex() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return 1, s.check()
}

func (s *Store) LastIndex() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return uint64(len(s.entries)), s.check()
}

func (s *Store) Snapshot() (*raftpb.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if err := s.check(); err != nil {
		return nil, err
	}

	return &raftpb.Snapshot{Metadata: &raftpb.SnapshotMetadata{ConfState: &raftpb.ConfState{}}}, nil
}
