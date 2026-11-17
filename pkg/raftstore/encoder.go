package raftstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"
)

const (
	MaxBatchBytes    = 16 << 20
	MaxBatchEntries  = 65536
	headerSize       = 64
	frameHeaderSize  = 24
	frameFooterSize  = 40
	updateHeaderSize = 28
	fileMagic        = "GODBRFT1"
	frameMagic       = "RAFTREC1"
	frameEnd         = "RAFTEND1"
)

func encodeHeader(identity Identity) ([]byte, [32]byte) {
	header := make([]byte, headerSize)
	copy(header, fileMagic)
	copy(header[8:24], identity.GroupID[:])
	binary.LittleEndian.PutUint64(header[24:32], identity.NodeID)
	digest := sha256.Sum256(header[:32])
	copy(header[32:], digest[:])

	return header, digest
}

func encodeUpdate(hard *raftpb.HardState, entries []*raftpb.Entry) ([]byte, error) {
	if len(entries) > MaxBatchEntries {
		return nil, ErrBatchTooLarge
	}

	size := updateHeaderSize

	for _, entry := range entries {
		n := proto.Size(entry)
		if n > MaxBatchBytes-size-4 {
			return nil, ErrBatchTooLarge
		}

		size += 4 + n
	}

	payload := make([]byte, updateHeaderSize, size)
	binary.LittleEndian.PutUint64(payload[:8], hard.GetTerm())
	binary.LittleEndian.PutUint64(payload[8:16], hard.GetVote())
	binary.LittleEndian.PutUint64(payload[16:24], hard.GetCommit())
	binary.LittleEndian.PutUint32(payload[24:28], uint32(len(entries))) // #nosec G115 -- Count is bounded by MaxBatchEntries (65536) above.
	encoder := proto.MarshalOptions{Deterministic: true}

	for _, entry := range entries {
		n := proto.Size(entry)
		payload = binary.LittleEndian.AppendUint32(payload, uint32(n)) // #nosec G115 -- Each entry was bounded by MaxBatchBytes above.

		var err error

		payload, err = encoder.MarshalAppend(payload, entry)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidUpdate, err)
		}
	}

	return payload, nil
}

func decodeUpdate(payload []byte) (*raftpb.HardState, []*raftpb.Entry, error) {
	if len(payload) < updateHeaderSize || len(payload) > MaxBatchBytes {
		return nil, nil, ErrCorrupt
	}

	hard := &raftpb.HardState{
		Term:   proto.Uint64(binary.LittleEndian.Uint64(payload[:8])),
		Vote:   proto.Uint64(binary.LittleEndian.Uint64(payload[8:16])),
		Commit: proto.Uint64(binary.LittleEndian.Uint64(payload[16:24])),
	}
	count := binary.LittleEndian.Uint32(payload[24:28])

	payload = payload[updateHeaderSize:]
	if count > MaxBatchEntries || uint64(count) > uint64(len(payload)/4) {
		return nil, nil, ErrCorrupt
	}

	var entries []*raftpb.Entry

	for range count {
		if len(payload) < 4 {
			return nil, nil, ErrCorrupt
		}

		n := binary.LittleEndian.Uint32(payload[:4])

		payload = payload[4:]
		if uint64(n) > uint64(len(payload)) {
			return nil, nil, ErrCorrupt
		}

		entry := &raftpb.Entry{}
		if err := proto.Unmarshal(payload[:n], entry); err != nil {
			return nil, nil, fmt.Errorf("%w: %w", ErrCorrupt, err)
		}

		entries = append(entries, entry)
		payload = payload[n:]
	}

	if len(payload) != 0 {
		return nil, nil, ErrCorrupt
	}

	return hard, entries, nil
}

func encodeFrame(payload []byte, previous [32]byte) ([]byte, [32]byte) {
	frame := make([]byte, frameHeaderSize, frameHeaderSize+len(payload)+frameFooterSize)
	copy(frame, frameMagic)
	binary.LittleEndian.PutUint64(frame[8:16], uint64(len(payload)))
	binary.LittleEndian.PutUint64(frame[16:24], ^uint64(len(payload)))
	frame = append(frame, payload...)
	frame = append(frame, frameEnd...)
	digest := frameDigest(previous, frame)
	frame = append(frame, digest[:]...)

	return frame, digest
}

func frameDigest(previous [32]byte, frame []byte) [32]byte {
	hash := sha256.New()
	_, _ = hash.Write(previous[:])
	_, _ = hash.Write(frame)

	var digest [32]byte

	copy(digest[:], hash.Sum(nil))

	return digest
}

func (s *Store) recover() error {
	info, err := s.file.Stat()
	if err != nil {
		return err
	}

	if !info.Mode().IsRegular() || info.Size() < headerSize {
		return ErrCorrupt
	}

	header := make([]byte, headerSize)
	if _, err := s.file.ReadAt(header, 0); err != nil {
		return err
	}

	digest := sha256.Sum256(header[:32])
	if string(header[:8]) != fileMagic || !bytes.Equal(digest[:], header[32:]) {
		return ErrCorrupt
	}

	expected, _ := encodeHeader(s.identity)
	if !bytes.Equal(header, expected) {
		return ErrIdentity
	}

	s.offset, s.digest = headerSize, digest
	for s.offset < info.Size() {
		remaining := info.Size() - s.offset
		if remaining < frameHeaderSize {
			break
		}

		prefix := make([]byte, frameHeaderSize)
		if _, err := s.file.ReadAt(prefix, s.offset); err != nil {
			return err
		}

		length := binary.LittleEndian.Uint64(prefix[8:16])
		if string(prefix[:8]) != frameMagic || binary.LittleEndian.Uint64(prefix[16:24]) != ^length || length < updateHeaderSize || length > MaxBatchBytes {
			return ErrCorrupt
		}

		size := int64(length) + frameHeaderSize + frameFooterSize
		if size > remaining {
			break
		}

		frame := make([]byte, size)
		if _, err := s.file.ReadAt(frame, s.offset); err != nil {
			return err
		}

		end := len(frame) - 32

		digest := frameDigest(s.digest, frame[:end])
		if string(frame[end-8:end]) != frameEnd || !bytes.Equal(digest[:], frame[end:]) {
			return ErrCorrupt
		}

		hard, entries, err := decodeUpdate(frame[frameHeaderSize : end-8])
		if err != nil {
			return err
		}

		if err := s.validateUpdate(hard, entries); err != nil {
			return fmt.Errorf("%w: %w", ErrCorrupt, err)
		}

		s.apply(hard, entries)
		s.offset += size
		s.digest = digest
	}

	if s.offset != info.Size() {
		if err := s.file.Truncate(s.offset); err != nil {
			return err
		}
	}

	return s.file.Sync()
}
