package raftstore

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"uuid"

	"github.com/stretchr/testify/require"
	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"
)

func testIdentity() Identity {
	return Identity{GroupID: uuid.UUID{1}, NodeID: 1}
}

func testStore(t testing.TB) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "node.raft")
	store, err := Create(path, testIdentity())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store, path
}

func testHard(term, vote, commit uint64) *raftpb.HardState {
	return &raftpb.HardState{Term: proto.Uint64(term), Vote: proto.Uint64(vote), Commit: proto.Uint64(commit)}
}

func testEntry(index, term uint64, data string) *raftpb.Entry {
	return &raftpb.Entry{Index: proto.Uint64(index), Term: proto.Uint64(term), Data: []byte(data)}
}

func seedStore(t testing.TB, store *Store) {
	t.Helper()
	require.NoError(t, store.Save(raft.Ready{HardState: testHard(1, 1, 1), Entries: []*raftpb.Entry{
		testEntry(1, 1, "one"), testEntry(2, 1, "two"), testEntry(3, 1, "three"),
	}}))
}

func assertState(t testing.TB, store *Store, term, vote, commit, last uint64) {
	t.Helper()
	hard, conf, err := store.InitialState()
	require.NoError(t, err)
	require.Equal(t, term, hard.GetTerm())
	require.Equal(t, vote, hard.GetVote())
	require.Equal(t, commit, hard.GetCommit())
	require.Empty(t, conf.GetVoters())
	index, err := store.LastIndex()
	require.NoError(t, err)
	require.Equal(t, last, index)
}

func TestStoreCreateAndOpen(t *testing.T) {
	store, path := testStore(t)
	require.Equal(t, testIdentity(), store.Identity())
	assertState(t, store, 0, 0, 0, 0)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	_, err = Open(path, testIdentity())
	require.ErrorIs(t, err, ErrLocked)
	_, err = Create(path, testIdentity())
	require.ErrorIs(t, err, os.ErrExist)
	_, err = Create(path+".invalid", Identity{})
	require.ErrorIs(t, err, ErrIdentity)
	_, err = Open(path+".missing", testIdentity())
	require.ErrorIs(t, err, os.ErrNotExist)

	alias := path + ".link"
	require.NoError(t, os.Symlink(path, alias))
	_, err = Open(alias, testIdentity())
	require.ErrorIs(t, err, ErrLocked)
	require.NoError(t, store.Close())
	_, err = Open(path, Identity{GroupID: uuid.UUID{2}, NodeID: 1})
	require.ErrorIs(t, err, ErrIdentity)
	_, err = Open(path, Identity{GroupID: uuid.UUID{1}, NodeID: 2})
	require.ErrorIs(t, err, ErrIdentity)
	reopened, err := Open(path, testIdentity())
	require.NoError(t, err)
	require.NoError(t, reopened.Close())
}

func TestStoreSaveAndReplaceUncommittedSuffix(t *testing.T) {
	store, path := testStore(t)
	seedStore(t, store)
	require.NoError(t, store.Save(raft.Ready{HardState: testHard(2, 0, 1), Entries: []*raftpb.Entry{testEntry(2, 2, "replacement")}}))
	assertState(t, store, 2, 0, 1, 2)
	require.NoError(t, store.Save(raft.Ready{HardState: testHard(2, 2, 2)}))
	require.NoError(t, store.Save(raft.Ready{Entries: []*raftpb.Entry{testEntry(3, 2, "new")}}))
	require.NoError(t, store.Save(raft.Ready{HardState: testHard(3, 0, 2)}))
	require.NoError(t, store.Close())
	reopened, err := Open(path, testIdentity())
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	assertState(t, reopened, 3, 0, 2, 3)
	entries, err := reopened.Entries(1, 4, math.MaxUint64)
	require.NoError(t, err)
	require.Equal(t, []byte("one"), entries[0].Data)
	require.Equal(t, []byte("replacement"), entries[1].Data)
	require.Equal(t, []byte("new"), entries[2].Data)
	require.NoError(t, reopened.Save(raft.Ready{HardState: testHard(3, 3, 3)}))
}

func TestStoreRejectsInvalidUpdatesWithoutWriting(t *testing.T) {
	tests := map[string]raft.Ready{
		"term regression":         {HardState: testHard(0, 1, 1)},
		"commit regression":       {HardState: testHard(1, 1, 0)},
		"vote changed":            {HardState: testHard(1, 2, 1)},
		"vote cleared":            {HardState: testHard(1, 0, 1)},
		"commit beyond log":       {HardState: testHard(1, 1, 4)},
		"gap":                     {Entries: []*raftpb.Entry{testEntry(5, 1, "gap")}},
		"zero index":              {Entries: []*raftpb.Entry{testEntry(0, 1, "zero")}},
		"zero term":               {Entries: []*raftpb.Entry{testEntry(4, 0, "zero")}},
		"nil entry":               {Entries: []*raftpb.Entry{nil}},
		"nonconsecutive":          {Entries: []*raftpb.Entry{testEntry(4, 1, "four"), testEntry(6, 1, "six")}},
		"future term":             {Entries: []*raftpb.Entry{testEntry(4, 2, "four")}},
		"term decreases in batch": {HardState: testHard(2, 1, 1), Entries: []*raftpb.Entry{testEntry(4, 2, "four"), testEntry(5, 1, "five")}},
		"committed rewrite":       {HardState: testHard(2, 1, 1), Entries: []*raftpb.Entry{testEntry(1, 2, "bad")}},
		"same term rewrite":       {Entries: []*raftpb.Entry{testEntry(2, 1, "bad")}},
		"unknown type":            {Entries: []*raftpb.Entry{{Index: proto.Uint64(4), Term: proto.Uint64(1), Type: raftpb.EntryType(99).Enum()}}},
	}
	for name, ready := range tests {
		t.Run(name, func(t *testing.T) {
			store, path := testStore(t)
			seedStore(t, store)
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			require.ErrorIs(t, store.Save(ready), ErrInvalidUpdate)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, before, after)
			assertState(t, store, 1, 1, 1, 3)
			require.NoError(t, store.Save(raft.Ready{HardState: testHard(1, 1, 3)}))
		})
	}
}

func TestStoreProtectsCommittedTail(t *testing.T) {
	store, _ := testStore(t)
	seedStore(t, store)
	require.NoError(t, store.Save(raft.Ready{HardState: testHard(1, 1, 3)}))
	require.ErrorIs(t, store.Save(raft.Ready{Entries: []*raftpb.Entry{testEntry(1, 1, "one")}}), ErrInvalidUpdate)
	require.NoError(t, store.Save(raft.Ready{Entries: []*raftpb.Entry{testEntry(2, 1, "two"), testEntry(3, 1, "three"), testEntry(4, 1, "four")}}))
	assertState(t, store, 1, 1, 3, 4)
}

func TestStoreUnsupportedAndOversizedUpdates(t *testing.T) {
	store, _ := testStore(t)
	require.NoError(t, store.Save(raft.Ready{}))
	require.Equal(t, int64(headerSize), store.offset)
	require.ErrorIs(t, store.Save(raft.Ready{Snapshot: &raftpb.Snapshot{Metadata: &raftpb.SnapshotMetadata{Index: proto.Uint64(1)}}}), ErrSnapshotUnsupported)
	require.ErrorIs(t, store.Save(raft.Ready{Messages: []*raftpb.Message{{Type: raftpb.MsgStorageAppend.Enum()}}}), ErrAsyncUnsupported)
	require.ErrorIs(t, store.Save(raft.Ready{HardState: testHard(1, 1, 0), Entries: []*raftpb.Entry{testEntry(1, 1, string(bytes.Repeat([]byte{1}, MaxBatchBytes)))}}), ErrBatchTooLarge)
	assertState(t, store, 0, 0, 0, 0)
}

func TestStorageBoundsAndOwnership(t *testing.T) {
	store, _ := testStore(t)
	entry := testEntry(1, 1, "one")
	hard := testHard(1, 1, 1)
	require.NoError(t, store.Save(raft.Ready{HardState: hard, Entries: []*raftpb.Entry{entry}}))
	entry.Data[0] = 'x'
	*entry.Term = 99
	*hard.Term = 99
	assertState(t, store, 1, 1, 1, 1)
	require.NoError(t, store.Save(raft.Ready{Entries: []*raftpb.Entry{testEntry(2, 1, "two")}}))

	for _, maxSize := range []uint64{0, 1, uint64(proto.Size(testEntry(1, 1, "one")))} {
		entries, err := store.Entries(1, 3, maxSize)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.Equal(t, []byte("one"), entries[0].Data)
		entries[0].Data[0] = 'z'
	}

	entries, err := store.Entries(1, 3, math.MaxUint64)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	_, err = store.Entries(0, 1, 0)
	require.ErrorIs(t, err, raft.ErrCompacted)

	for _, bounds := range [][2]uint64{{2, 1}, {1, 4}, {4, 4}, {math.MaxUint64, math.MaxUint64}} {
		_, err = store.Entries(bounds[0], bounds[1], 0)
		require.ErrorIs(t, err, raft.ErrUnavailable)
	}

	entries, err = store.Entries(3, 3, 0)
	require.NoError(t, err)
	require.Empty(t, entries)
	term, err := store.Term(0)
	require.NoError(t, err)
	require.Zero(t, term)
	term, err = store.Term(1)
	require.NoError(t, err)
	require.Equal(t, uint64(1), term)
	_, err = store.Term(3)
	require.ErrorIs(t, err, raft.ErrUnavailable)
	first, err := store.FirstIndex()
	require.NoError(t, err)
	require.Equal(t, uint64(1), first)
	snapshot, err := store.Snapshot()
	require.NoError(t, err)
	require.True(t, raft.IsEmptySnap(snapshot))
	hard, _, err = store.InitialState()
	require.NoError(t, err)
	*hard.Term = 99
	assertState(t, store, 1, 1, 1, 2)
}

func TestStoreClosed(t *testing.T) {
	store, _ := testStore(t)
	require.NoError(t, store.Close())
	require.NoError(t, store.Close())
	require.ErrorIs(t, store.Save(raft.Ready{}), ErrClosed)
	_, _, err := store.InitialState()
	require.ErrorIs(t, err, ErrClosed)
	_, err = store.Entries(1, 1, 0)
	require.ErrorIs(t, err, ErrClosed)
	_, err = store.Term(0)
	require.ErrorIs(t, err, ErrClosed)
	_, err = store.FirstIndex()
	require.ErrorIs(t, err, ErrClosed)
	_, err = store.LastIndex()
	require.ErrorIs(t, err, ErrClosed)
	_, err = store.Snapshot()
	require.ErrorIs(t, err, ErrClosed)
}

func TestStoreConcurrentReadsAndWrites(t *testing.T) {
	store, _ := testStore(t)
	seedStore(t, store)

	var group sync.WaitGroup

	for range 4 {
		group.Go(func() {
			for range 100 {
				_, _, err := store.InitialState()
				if err != nil {
					t.Error(err)
					return
				}

				entries, err := store.Entries(1, 2, 0)
				if err != nil {
					t.Error(err)
					return
				}

				entries[0].Data[0] = 'x'
			}
		})
	}

	for index := uint64(4); index < 20; index++ {
		require.NoError(t, store.Save(raft.Ready{Entries: []*raftpb.Entry{testEntry(index, 1, "value")}}))
	}

	group.Wait()
}
