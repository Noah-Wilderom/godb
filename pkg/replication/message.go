package replication

import (
	"fmt"

	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/raftstore"
)

const (
	MaxMessageBytes = raftstore.MaxBatchBytes / 2
)

func (n *Node) validateMessage(message Message) error {
	m := message.Raft
	if message.GroupID != n.identity.GroupID || m == nil || !validNodeID(m.GetFrom()) || m.GetFrom() == n.identity.NodeID || m.GetTo() != n.identity.NodeID || m.GetTerm() == 0 {
		return ErrInvalidMessage
	}

	if m.GetType() == raftpb.MsgSnap {
		return raftstore.ErrSnapshotUnsupported
	}

	switch m.GetType() {
	case raftpb.MsgApp, raftpb.MsgAppResp, raftpb.MsgVote, raftpb.MsgVoteResp, raftpb.MsgPreVote, raftpb.MsgPreVoteResp, raftpb.MsgHeartbeat, raftpb.MsgHeartbeatResp:
	default:
		return ErrInvalidMessage
	}

	if len(m.Entries) > raftstore.MaxBatchEntries || proto.Size(m) > MaxMessageBytes || m.Snapshot != nil {
		return ErrInvalidMessage
	}

	if m.GetType() != raftpb.MsgApp && len(m.Entries) != 0 {
		return ErrInvalidMessage
	}

	previousTerm := m.GetLogTerm()
	for i, entry := range m.Entries {
		if entry.GetIndex() <= m.GetIndex() || entry.GetIndex()-m.GetIndex() != uint64(i)+1 || entry.GetTerm() < previousTerm || entry.GetTerm() > m.GetTerm() {
			return ErrInvalidMessage
		}

		if _, _, err := decodeEntry(entry); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidMessage, err)
		}

		previousTerm = entry.GetTerm()
	}

	return nil
}

func decodeEntry(entry *raftpb.Entry) (database.LogEntry, *raftpb.ConfChange, error) {
	if entry.GetIndex() == 0 || entry.GetTerm() == 0 {
		return database.LogEntry{}, nil, ErrInvalidEntry
	}

	logged := database.LogEntry{Index: entry.GetIndex(), Term: entry.GetTerm()}

	switch entry.GetType() {
	case raftpb.EntryNormal:
		if len(entry.Data) != 0 {
			requestID, command, err := DecodeProposal(entry.Data)
			if err != nil {
				return database.LogEntry{}, nil, err
			}

			logged.RequestID, logged.Command = requestID, command
		}
	case raftpb.EntryConfChange:
		change := &raftpb.ConfChange{}
		if err := proto.Unmarshal(entry.Data, change); err != nil {
			return database.LogEntry{}, nil, fmt.Errorf("%w: %w", ErrInvalidEntry, err)
		}

		if change.GetType() != raftpb.ConfChangeAddNode || !validNodeID(change.GetNodeId()) || len(change.Context) != 0 || entry.GetTerm() != 1 || entry.GetIndex() > 64 {
			return database.LogEntry{}, nil, ErrInvalidEntry
		}

		return logged, change, nil
	default:
		return database.LogEntry{}, nil, ErrInvalidEntry
	}

	return logged, nil, nil
}
