package replication

import (
	"bytes"
	"testing"
	"uuid"

	"github.com/stretchr/testify/require"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"

	"github.com/Noah-Wilderom/godb/pkg/raftstore"
)

func TestProposalEncodingAndOwnership(t *testing.T) {
	id := uuid.UUID{7}
	command := commandPut(t, "key", "value")
	original := bytes.Clone(command)
	encoded, err := EncodeProposal(id, command)
	require.NoError(t, err)
	command[len(command)-1] ^= 1
	decodedID, decoded, err := DecodeProposal(encoded)
	require.NoError(t, err)
	require.Equal(t, id, decodedID)
	require.Equal(t, original, decoded)
	decoded[len(decoded)-1] ^= 1
	_, decoded, err = DecodeProposal(encoded)
	require.NoError(t, err)
	require.Equal(t, original, decoded)

	for length := 0; length < len(encoded); length++ {
		_, _, err := DecodeProposal(encoded[:length])
		require.Error(t, err)
	}

	for _, offset := range []int{0, 7, commandHeaderSize} {
		bad := bytes.Clone(encoded)
		bad[offset] ^= 1
		_, _, err := DecodeProposal(bad)
		require.ErrorIs(t, err, ErrInvalidProposal)
	}

	bad := bytes.Clone(encoded)
	clear(bad[8:24])
	_, _, err = DecodeProposal(bad)
	require.ErrorIs(t, err, ErrInvalidProposal)
	_, _, err = DecodeProposal(append(encoded, 0))
	require.ErrorIs(t, err, ErrInvalidProposal)
	_, err = EncodeProposal(uuid.Nil(), original)
	require.ErrorIs(t, err, ErrInvalidProposal)
	_, err = EncodeProposal(id, nil)
	require.ErrorIs(t, err, ErrInvalidProposal)
	_, _, err = DecodeProposal(make([]byte, MaxProposalBytes+1))
	require.ErrorIs(t, err, ErrInvalidProposal)
}

func TestNodeRejectsPeerInputBeforeRaftMutation(t *testing.T) {
	f := newFixture(t, 1)
	f.start(t, 1, 2)
	good := &raftpb.Message{Type: raftpb.MsgHeartbeat.Enum(), From: proto.Uint64(2), To: proto.Uint64(1), Term: proto.Uint64(2)}

	for name, mutate := range map[string]func(*Message){
		"nil":                func(m *Message) { m.Raft = nil },
		"wrong group":        func(m *Message) { m.GroupID = uuid.UUID{9} },
		"wrong destination":  func(m *Message) { m.Raft.To = proto.Uint64(3) },
		"self":               func(m *Message) { m.Raft.From = proto.Uint64(1) },
		"no sender":          func(m *Message) { m.Raft.From = nil },
		"reserved sender":    func(m *Message) { m.Raft.From = proto.Uint64(^uint64(0)) },
		"no term":            func(m *Message) { m.Raft.Term = nil },
		"local event":        func(m *Message) { m.Raft.Type = raftpb.MsgHup.Enum() },
		"remote proposal":    func(m *Message) { m.Raft.Type = raftpb.MsgProp.Enum() },
		"unknown type":       func(m *Message) { m.Raft.Type = raftpb.MessageType(99).Enum() },
		"unexpected entries": func(m *Message) { m.Raft.Entries = []*raftpb.Entry{{}} },
		"bad command": func(m *Message) {
			m.Raft.Type = raftpb.MsgApp.Enum()
			m.Raft.Entries = []*raftpb.Entry{{Term: proto.Uint64(2), Index: proto.Uint64(1), Data: []byte("bad")}}
		},
		"gap": func(m *Message) {
			m.Raft.Type = raftpb.MsgApp.Enum()
			m.Raft.Entries = []*raftpb.Entry{{Term: proto.Uint64(2), Index: proto.Uint64(2)}}
		},
		"unsupported membership": func(m *Message) {
			m.Raft.Type = raftpb.MsgApp.Enum()
			m.Raft.Entries = []*raftpb.Entry{{Term: proto.Uint64(2), Index: proto.Uint64(1), Type: raftpb.EntryConfChangeV2.Enum()}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			before, err := f.node.Status()
			require.NoError(t, err)
			message := Message{GroupID: f.identity.GroupID, Raft: proto.Clone(good).(*raftpb.Message)}
			mutate(&message)
			output, err := f.node.Step(message)
			require.ErrorIs(t, err, ErrInvalidMessage)
			require.Empty(t, output)
			after, err := f.node.Status()
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}

	good.Type = raftpb.MsgSnap.Enum()
	_, err := f.node.Step(Message{GroupID: f.identity.GroupID, Raft: good})
	require.ErrorIs(t, err, raftstore.ErrSnapshotUnsupported)
}

func FuzzDecodeProposal(f *testing.F) {
	encoded, err := EncodeProposal(uuid.UUID{1}, commandPut(f, "key", "value"))
	if err != nil {
		f.Fatal(err)
	}

	f.Add(encoded)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		id, command, err := DecodeProposal(data)
		if err != nil {
			return
		}

		encoded, err := EncodeProposal(id, command)
		require.NoError(t, err)
		require.Equal(t, data, encoded)
	})
}
