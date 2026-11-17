package replication

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"uuid"

	"github.com/stretchr/testify/require"
	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/raftstore"
)

type fixture struct {
	node     *Node
	store    *raftstore.Store
	db       *database.Database
	dbPath   string
	logPath  string
	identity raftstore.Identity
}

func newFixture(t testing.TB, id uint64) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{dbPath: filepath.Join(dir, "data.db"), logPath: filepath.Join(dir, "node.raft"), identity: raftstore.Identity{GroupID: uuid.UUID{1}, NodeID: id}}
	require.NoError(t, database.Create(f.dbPath))

	var err error

	f.db, err = database.Open(f.dbPath)
	require.NoError(t, err)
	f.store, err = raftstore.Create(f.logPath, f.identity)
	require.NoError(t, err)
	t.Cleanup(func() { f.close(t) })
	return f
}

func (f *fixture) close(t testing.TB) {
	t.Helper()

	if f.node != nil {
		f.node.Close()
	}

	require.NoError(t, f.db.Close())
	require.NoError(t, f.store.Close())
}

func (f *fixture) start(t testing.TB, peers ...uint64) Output {
	t.Helper()
	node, output, err := New(Config{Store: f.store, Database: f.db, BootstrapPeers: peers})
	require.NoError(t, err)
	f.node = node
	return output
}

func (f *fixture) reopen(t testing.TB) Output {
	t.Helper()
	f.close(t)

	var err error

	f.db, err = database.Open(f.dbPath)
	require.NoError(t, err)
	f.store, err = raftstore.Open(f.logPath, f.identity)
	require.NoError(t, err)
	return f.start(t)
}

func commandPut(t testing.TB, key, value string) []byte {
	t.Helper()
	command, err := database.EncodeCommand(database.Command{Operations: []database.CommandOperation{{Kind: database.CommandPut, Key: []byte(key), Value: []byte(value)}}})
	require.NoError(t, err)
	return command
}

func assertValue(t testing.TB, db *database.Database, key, value string) {
	t.Helper()
	data, found, err := db.Get([]byte(key))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []byte(value), data)
}

func TestNodeSingleMemberRetryAndRestart(t *testing.T) {
	f := newFixture(t, 1)
	output := f.start(t, 1)
	require.Empty(t, output.Results)
	position, err := f.db.AppliedPosition()
	require.NoError(t, err)
	require.Equal(t, uint64(1), position.Index)
	require.ErrorIs(t, f.db.Put([]byte("outside"), []byte("log")), database.ErrReplicatedWriteRequired)
	_, err = f.node.Campaign()
	require.NoError(t, err)
	id := uuid.UUID{2}
	command := commandPut(t, "key", "one")
	output, err = f.node.Propose(id, command)
	require.NoError(t, err)
	require.Len(t, output.Results, 1)
	require.Equal(t, database.CommandOK, output.Results[0].Command.Code)
	require.Equal(t, id, output.Results[0].RequestID)
	require.Equal(t, uint64(3), output.Results[0].Index)
	_, err = f.node.Propose(uuid.UUID{3}, commandPut(t, "key", "two"))
	require.NoError(t, err)
	output, err = f.node.Propose(id, command)
	require.NoError(t, err)
	require.Equal(t, database.CommandOK, output.Results[0].Command.Code)
	assertValue(t, f.db, "key", "two")
	output, err = f.node.Propose(id, commandPut(t, "key", "three"))
	require.NoError(t, err)
	require.Equal(t, database.CommandRequestIDConflict, output.Results[0].Command.Code)
	assertValue(t, f.db, "key", "two")
	status, err := f.node.Status()
	require.NoError(t, err)
	require.Equal(t, status.Commit, status.Applied)
	output = f.reopen(t)
	require.Empty(t, output.Results)
	require.Zero(t, f.db.Stats().Writes)
	assertValue(t, f.db, "key", "two")
	_, err = f.node.Campaign()
	require.NoError(t, err)
	output, err = f.node.Propose(uuid.UUID{4}, commandPut(t, "key", "after"))
	require.NoError(t, err)
	require.Len(t, output.Results, 1)
	assertValue(t, f.db, "key", "after")
}

type hookedStorage struct {
	Storage

	save func(raft.Ready) error
}

func (s *hookedStorage) Save(ready raft.Ready) error {
	if s.save != nil {
		return s.save(ready)
	}

	return s.Storage.Save(ready)
}

type hookedDatabase struct {
	StateMachine

	apply func(database.LogEntry) (database.CommandResult, error)
}

func (db *hookedDatabase) ApplyLogEntry(entry database.LogEntry) (database.CommandResult, error) {
	if db.apply != nil {
		return db.apply(entry)
	}

	return db.StateMachine.ApplyLogEntry(entry)
}

func TestNodeFailureBetweenDurableBoundaries(t *testing.T) {
	for _, phase := range []string{"before log save", "after log save", "before database apply", "after database apply"} {
		t.Run(phase, func(t *testing.T) {
			f := newFixture(t, 1)
			store := &hookedStorage{Storage: f.store}
			db := &hookedDatabase{StateMachine: f.db}
			node, _, err := New(Config{Store: store, Database: db, BootstrapPeers: []uint64{1}})
			require.NoError(t, err)
			f.node = node
			_, err = node.Campaign()
			require.NoError(t, err)
			injected := errors.New("injected failure")
			store.save = func(ready raft.Ready) error {
				if phase == "before log save" {
					return injected
				}

				if err := f.store.Save(ready); err != nil {
					return err
				}

				if phase == "after log save" {
					return injected
				}

				return nil
			}
			db.apply = func(entry database.LogEntry) (database.CommandResult, error) {
				if phase == "before database apply" {
					return database.CommandResult{}, injected
				}

				result, err := f.db.ApplyLogEntry(entry)
				if err != nil {
					return result, err
				}

				if phase == "after database apply" {
					return database.CommandResult{}, injected
				}

				return result, nil
			}
			id := uuid.UUID{8}
			output, err := node.Propose(id, commandPut(t, "key", "value"))
			require.ErrorIs(t, err, ErrStopped)
			require.ErrorIs(t, err, injected)
			require.Empty(t, output.Messages)
			require.Empty(t, output.Results)
			_, err = node.Tick()
			require.ErrorIs(t, err, ErrStopped)
			_, err = node.Status()
			require.ErrorIs(t, err, ErrStopped)
			_, err = node.Propose(id, commandPut(t, "key", "other"))
			require.ErrorIs(t, err, ErrStopped)
			f.reopen(t)

			if phase == "before log save" {
				_, found, err := f.db.Get([]byte("key"))
				require.NoError(t, err)
				require.False(t, found)
			} else {
				// A log append can be durable before Raft emits the commit update.
				_, err = f.node.Campaign()
				require.NoError(t, err)
				assertValue(t, f.db, "key", "value")
				result, found, err := f.db.AppliedRequest(id)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, database.CommandOK, result.Code)
			}
		})
	}
}

func TestNodeSavesBeforeApplyAndReturnsOnlyDurableResults(t *testing.T) {
	f := newFixture(t, 1)
	db := &hookedDatabase{StateMachine: f.db, apply: func(entry database.LogEntry) (database.CommandResult, error) {
		hard, _, err := f.store.InitialState()
		require.NoError(t, err)
		require.GreaterOrEqual(t, hard.GetCommit(), entry.Index)
		term, err := f.store.Term(entry.Index)
		require.NoError(t, err)
		require.Equal(t, entry.Term, term)
		return f.db.ApplyLogEntry(entry)
	}}
	node, _, err := New(Config{Store: f.store, Database: db, BootstrapPeers: []uint64{1}})
	require.NoError(t, err)
	f.node = node
	_, err = node.Campaign()
	require.NoError(t, err)
	output, err := node.Propose(uuid.UUID{9}, commandPut(t, "key", "value"))
	require.NoError(t, err)
	require.Len(t, output.Results, 1)
	position, err := f.db.AppliedPosition()
	require.NoError(t, err)
	require.Equal(t, output.Results[0].Index, position.Index)
	assertValue(t, f.db, "key", "value")
}

func TestNodeRejectsMismatchedDatabaseAndLog(t *testing.T) {
	for _, kind := range []string{"group", "ahead", "term", "contents"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t, 1)
			id := f.identity.GroupID
			term := uint64(1)

			if kind == "group" {
				id = uuid.UUID{9}
			}

			if kind == "term" {
				term = 2
			}

			entry := database.LogEntry{GroupID: id, Index: 1, Term: term}
			if kind == "contents" {
				entry.RequestID = uuid.UUID{7}
				entry.Command = commandPut(t, "key", "value")
			}

			_, err := f.db.ApplyLogEntry(entry)
			require.NoError(t, err)

			if kind != "ahead" {
				require.NoError(t, f.store.Save(raft.Ready{HardState: &raftpb.HardState{Term: proto.Uint64(1), Commit: proto.Uint64(1)}, Entries: []*raftpb.Entry{{Index: proto.Uint64(1), Term: proto.Uint64(1)}}}))
			}

			_, _, err = New(Config{Store: f.store, Database: f.db})
			if kind == "contents" {
				require.ErrorIs(t, err, database.ErrLogConflict)
			} else {
				require.ErrorIs(t, err, ErrStateMismatch)
			}
		})
	}
}

func TestNodeConfigAndClose(t *testing.T) {
	f := newFixture(t, 1)
	_, _, err := New(Config{})
	require.ErrorIs(t, err, ErrInvalidConfig)

	for _, peers := range [][]uint64{{0, 1}, {1, 1}, {2, 3}, {1, ^uint64(0)}} {
		_, _, err := New(Config{Store: f.store, Database: f.db, BootstrapPeers: peers})
		require.ErrorIs(t, err, ErrInvalidConfig)
	}

	f.start(t, 1)
	_, _, err = New(Config{Store: f.store, Database: f.db, BootstrapPeers: []uint64{1}})
	require.ErrorIs(t, err, ErrInvalidConfig)
	_, err = f.node.Propose(uuid.UUID{2}, commandPut(t, "key", "value"))
	require.ErrorIs(t, err, ErrNotLeader)
	_, err = f.node.Campaign()
	require.NoError(t, err)
	_, err = f.node.Propose(uuid.Nil(), commandPut(t, "key", "value"))
	require.ErrorIs(t, err, ErrInvalidProposal)
	f.node.Close()
	f.node.Close()
	_, err = f.node.Tick()
	require.ErrorIs(t, err, ErrClosed)
	_, err = f.node.Campaign()
	require.ErrorIs(t, err, ErrClosed)
	_, err = f.node.Step(Message{})
	require.ErrorIs(t, err, ErrClosed)
	_, err = f.node.Status()
	require.ErrorIs(t, err, ErrClosed)
	_, err = f.node.Propose(uuid.UUID{2}, nil)
	require.ErrorIs(t, err, ErrClosed)
	_, _, err = f.store.InitialState()
	require.NoError(t, err)
	_, err = f.db.AppliedPosition()
	require.NoError(t, err)
}

func TestNodeConcurrentEvents(t *testing.T) {
	f := newFixture(t, 1)
	f.start(t, 1)
	_, err := f.node.Campaign()
	require.NoError(t, err)
	command := commandPut(t, "key", "value")

	var group sync.WaitGroup

	for worker := byte(1); worker <= 4; worker++ {
		group.Go(func() {
			for i := byte(0); i < 10; i++ {
				if _, err := f.node.Propose(uuid.UUID{worker, i}, command); err != nil {
					t.Error(err)
					return
				}

				if _, err := f.node.Tick(); err != nil {
					t.Error(err)
					return
				}

				if _, err := f.node.Status(); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}

	group.Wait()
	assertValue(t, f.db, "key", "value")
}
