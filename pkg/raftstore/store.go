package raftstore

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
	"uuid"

	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"

	"github.com/Noah-Wilderom/godb/internal/observe"
)

type Identity struct {
	GroupID uuid.UUID
	NodeID  uint64
}

type journal interface {
	ReadAt([]byte, int64) (int, error)
	WriteAt([]byte, int64) (int, error)
	Stat() (os.FileInfo, error)
	Truncate(int64) error
	Sync() error
	Close() error
}

type Store struct {
	metrics  *storeMetrics
	mu       sync.RWMutex
	file     journal
	identity Identity
	hard     *raftpb.HardState
	entries  []*raftpb.Entry
	offset   int64
	digest   [32]byte
	closed   bool
	failed   error
}

var _ raft.Storage = (*Store)(nil)

func Create(path string, identity Identity) (_ *Store, err error) {
	if err := validateIdentity(identity); err != nil {
		return nil, err
	}

	file, err := os.CreateTemp(filepath.Dir(path), ".godb-raft-*")
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = os.Remove(file.Name())
		if err != nil {
			err = errors.Join(err, file.Close())
		}
	}()

	if err := lock(file); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLocked, err)
	}

	header, digest := encodeHeader(identity)
	if err := writeAt(file, header, 0); err != nil {
		return nil, err
	}

	if err := file.Sync(); err != nil {
		return nil, err
	}

	if err := os.Link(file.Name(), path); err != nil {
		return nil, err
	}

	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}

	metrics := newMetrics()
	metrics.bytes.Set(headerSize)
	return &Store{file: file, identity: identity, hard: &raftpb.HardState{}, offset: headerSize, digest: digest, metrics: metrics}, nil
}

func Open(path string, identity Identity) (_ *Store, err error) {
	if err := validateIdentity(identity); err != nil {
		return nil, err
	}

	file, err := os.OpenFile(path, os.O_RDWR, 0o600) // #nosec G304 -- The local caller selects the Raft journal path.
	if err != nil {
		return nil, err
	}

	defer func() {
		if err != nil {
			err = errors.Join(err, file.Close())
		}
	}()

	if err := lock(file); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLocked, err)
	}

	store := &Store{file: file, identity: identity, hard: &raftpb.HardState{}, metrics: newMetrics()}
	if err := store.recover(); err != nil {
		return nil, err
	}

	store.metrics.bytes.Set(float64(store.offset))
	store.metrics.last.Set(float64(len(store.entries)))
	return store, nil
}

func (s *Store) Identity() Identity {
	return s.identity
}

func (s *Store) Save(ready raft.Ready) (err error) {
	start := time.Now()
	defer func() {
		if s.metrics != nil {
			observe.Since(s.metrics.save, start)

			if err != nil {
				s.metrics.errors.Inc()
			}
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.check(); err != nil {
		return err
	}

	if !raft.IsEmptySnap(ready.Snapshot) {
		return ErrSnapshotUnsupported
	}

	for _, message := range ready.Messages {
		if message.GetType() == raftpb.MsgStorageAppend || message.GetType() == raftpb.MsgStorageApply {
			return ErrAsyncUnsupported
		}
	}

	if raft.IsEmptyHardState(ready.HardState) && len(ready.Entries) == 0 {
		return nil
	}

	hard := s.hard
	if !raft.IsEmptyHardState(ready.HardState) {
		hard = ready.HardState
	}

	if err := s.validateUpdate(hard, ready.Entries); err != nil {
		return err
	}

	payload, err := encodeUpdate(hard, ready.Entries)
	if err != nil {
		return err
	}

	ownedHard, ownedEntries, err := decodeUpdate(payload)
	if err != nil {
		return err
	}

	frame, digest := encodeFrame(payload, s.digest)
	if s.offset > math.MaxInt64-int64(len(frame)) {
		return ErrBatchTooLarge
	}

	if err := writeAt(s.file, frame, s.offset); err != nil {
		return s.fail(err)
	}

	syncStart := time.Now()

	syncErr := s.file.Sync()
	if s.metrics != nil {
		observe.Since(s.metrics.sync, syncStart)
	}

	if err := syncErr; err != nil {
		return s.fail(err)
	}

	s.apply(ownedHard, ownedEntries)
	s.offset += int64(len(frame))

	s.digest = digest
	if s.metrics != nil {
		s.metrics.saves.Inc()
		s.metrics.bytes.Set(float64(s.offset))
		s.metrics.last.Set(float64(len(s.entries)))
	}

	return nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	s.closed = true

	return s.file.Close()
}

func (s *Store) check() error {
	if s.closed {
		return ErrClosed
	}

	return s.failed
}

func (s *Store) fail(err error) error {
	s.failed = fmt.Errorf("%w: %w", ErrFailed, err)
	return s.failed
}

func (s *Store) apply(hard *raftpb.HardState, entries []*raftpb.Entry) {
	if len(entries) != 0 {
		last := entries[len(entries)-1].GetIndex()
		if last < uint64(len(s.entries)) {
			clear(s.entries[last:])
		}

		s.entries = append(s.entries[:entries[0].GetIndex()-1], entries...)
	}

	s.hard = hard
}

func validateIdentity(identity Identity) error {
	if identity.GroupID == (uuid.UUID{}) || identity.NodeID == 0 {
		return ErrIdentity
	}

	return nil
}

func writeAt(file journal, data []byte, offset int64) error {
	n, err := file.WriteAt(data, offset)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}

	return err
}

func syncDirectory(path string) error {
	file, err := os.Open(path) // #nosec G304 -- Sync the directory containing the caller-selected journal.
	if err != nil {
		return err
	}

	return errors.Join(file.Sync(), file.Close())
}

func (s *Store) validateUpdate(hard *raftpb.HardState, entries []*raftpb.Entry) error {
	if hard.GetTerm() < s.hard.GetTerm() || hard.GetCommit() < s.hard.GetCommit() {
		return fmt.Errorf("%w: term or commit regressed", ErrInvalidUpdate)
	}

	if hard.GetTerm() == s.hard.GetTerm() && s.hard.GetVote() != 0 && hard.GetVote() != s.hard.GetVote() {
		return fmt.Errorf("%w: changed vote in the same term", ErrInvalidUpdate)
	}

	last := uint64(len(s.entries))
	if len(entries) != 0 {
		first := entries[0].GetIndex()
		if first == 0 || first > last+1 {
			return fmt.Errorf("%w: gap or zero index", ErrInvalidUpdate)
		}

		previousTerm := uint64(0)
		if first > 1 {
			previousTerm = s.entries[first-2].GetTerm()
		}

		for i, entry := range entries {
			if entry.GetIndex() != first+uint64(i) || entry.GetIndex() >= math.MaxInt || entry.GetTerm() == 0 || entry.GetTerm() < previousTerm {
				return fmt.Errorf("%w: invalid index or term", ErrInvalidUpdate)
			}

			switch entry.GetType() {
			case raftpb.EntryType_EntryNormal, raftpb.EntryType_EntryConfChange, raftpb.EntryType_EntryConfChangeV2:
			default:
				return fmt.Errorf("%w: unknown entry type", ErrInvalidUpdate)
			}

			if entry.GetIndex() <= last {
				old := s.entries[entry.GetIndex()-1]
				if (entry.GetIndex() <= s.hard.GetCommit() || old.GetTerm() == entry.GetTerm()) && !proto.Equal(old, entry) {
					return fmt.Errorf("%w: changed committed or same-term entry", ErrInvalidUpdate)
				}
			}

			previousTerm = entry.GetTerm()
		}

		last = entries[len(entries)-1].GetIndex()

		if previousTerm > hard.GetTerm() {
			return fmt.Errorf("%w: entry term exceeds current term", ErrInvalidUpdate)
		}
	}

	if hard.GetCommit() > last || (hard.GetTerm() == 0 && hard.GetVote() != 0) {
		return fmt.Errorf("%w: commit exceeds log or vote has no term", ErrInvalidUpdate)
	}

	return nil
}
