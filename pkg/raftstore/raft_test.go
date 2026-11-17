package raftstore

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"
)

func newTestNode(t *testing.T, store *Store) *raft.RawNode {
	t.Helper()
	node, err := raft.NewRawNode(&raft.Config{
		ID: store.Identity().NodeID, ElectionTick: 10, HeartbeatTick: 1,
		Storage: store, MaxSizePerMsg: 1 << 20, MaxInflightMsgs: 64,
	})
	require.NoError(t, err)
	return node
}

func TestStoreRaftReplacesPartitionedLeaderSuffix(t *testing.T) {
	nodes := make(map[uint64]*raft.RawNode)
	stores := make(map[uint64]*Store)
	peers := []raft.Peer{{ID: 1}, {ID: 2}, {ID: 3}}

	for id := uint64(1); id <= 3; id++ {
		identity := testIdentity()
		identity.NodeID = id
		store, err := Create(filepath.Join(t.TempDir(), "node.raft"), identity)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		stores[id] = store
		nodes[id] = newTestNode(t, store)
		require.NoError(t, nodes[id].Bootstrap(peers))
	}

	isolated := uint64(0)
	pump := func() {
		for step := 0; ; step++ {
			require.Less(t, step, 1000)

			var messages []*raftpb.Message

			progress := false

			for id := uint64(1); id <= 3; id++ {
				node := nodes[id]
				if !node.HasReady() {
					continue
				}

				progress = true
				ready := node.Ready()
				require.NoError(t, stores[id].Save(ready))

				for _, entry := range ready.CommittedEntries {
					if entry.GetType() == raftpb.EntryConfChange {
						change := &raftpb.ConfChange{}
						require.NoError(t, proto.Unmarshal(entry.Data, change))
						node.ApplyConfChange(change)
					}
				}

				messages = append(messages, ready.Messages...)
				node.Advance(ready)
			}

			for _, message := range messages {
				if message.GetTo() == isolated || message.GetFrom() == isolated {
					continue
				}

				require.NoError(t, nodes[message.GetTo()].Step(message))
			}

			if !progress {
				return
			}
		}
	}

	pump()
	require.NoError(t, nodes[1].Campaign())
	pump()
	require.Equal(t, raft.StateLeader, nodes[1].BasicStatus().RaftState)
	isolated = 1
	require.NoError(t, nodes[1].Propose([]byte("orphan")))
	pump()
	orphanIndex, err := stores[1].LastIndex()
	require.NoError(t, err)
	hard, _, err := stores[1].InitialState()
	require.NoError(t, err)
	require.Less(t, hard.GetCommit(), orphanIndex)
	require.NoError(t, nodes[2].Campaign())
	pump()
	require.Equal(t, raft.StateLeader, nodes[2].BasicStatus().RaftState)
	require.NoError(t, nodes[2].Propose([]byte("majority")))
	pump()
	isolated = 0

	for range 12 {
		nodes[2].Tick()
	}

	pump()
	require.NoError(t, nodes[2].Propose([]byte("healed")))
	pump()

	for id := uint64(1); id <= 3; id++ {
		last, err := stores[id].LastIndex()
		require.NoError(t, err)
		entries, err := stores[id].Entries(1, last+1, math.MaxUint64)
		require.NoError(t, err)

		for _, entry := range entries {
			require.NotEqual(t, []byte("orphan"), entry.Data)
		}

		require.Equal(t, []byte("healed"), entries[len(entries)-1].Data)
		hard, _, err := stores[id].InitialState()
		require.NoError(t, err)
		require.Equal(t, last, hard.GetCommit())
	}
}

func drainReady(t *testing.T, node *raft.RawNode, store *Store) []*raftpb.Entry {
	t.Helper()

	var applied []*raftpb.Entry

	for i := 0; node.HasReady(); i++ {
		require.Less(t, i, 100)
		ready := node.Ready()
		require.NoError(t, store.Save(ready))

		for _, entry := range ready.CommittedEntries {
			switch entry.GetType() {
			case raftpb.EntryConfChange:
				change := &raftpb.ConfChange{}
				require.NoError(t, proto.Unmarshal(entry.Data, change))
				node.ApplyConfChange(change)
			case raftpb.EntryConfChangeV2:
				change := &raftpb.ConfChangeV2{}
				require.NoError(t, proto.Unmarshal(entry.Data, change))
				node.ApplyConfChange(change)
			}

			applied = append(applied, entry)
		}

		node.Advance(ready)
	}

	return applied
}

func TestStoreRaftBootstrapCommitAndRestart(t *testing.T) {
	store, path := testStore(t)
	node := newTestNode(t, store)
	require.NoError(t, node.Bootstrap([]raft.Peer{{ID: 1}}))
	drainReady(t, node, store)
	require.NoError(t, node.Campaign())
	drainReady(t, node, store)
	require.NoError(t, node.Propose([]byte("command")))
	applied := drainReady(t, node, store)
	require.Len(t, applied, 1)
	require.Equal(t, []byte("command"), applied[0].Data)
	committedIndex := applied[0].GetIndex()
	oldTerm := applied[0].GetTerm()
	require.NoError(t, store.Close())

	recovered, err := Open(path, testIdentity())
	require.NoError(t, err)
	defer func() { require.NoError(t, recovered.Close()) }()
	restarted := newTestNode(t, recovered)
	replayed := drainReady(t, restarted, recovered)
	require.Len(t, replayed, int(committedIndex))
	require.Equal(t, []byte("command"), replayed[len(replayed)-1].Data)
	require.NoError(t, restarted.Campaign())
	drainReady(t, restarted, recovered)
	require.NoError(t, restarted.Propose([]byte("after restart")))
	applied = drainReady(t, restarted, recovered)
	require.Len(t, applied, 1)
	require.Equal(t, []byte("after restart"), applied[0].Data)
	require.Greater(t, applied[0].GetIndex(), committedIndex)
	require.Greater(t, applied[0].GetTerm(), oldTerm)
}
