package replication

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"uuid"

	"github.com/stretchr/testify/require"
	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/raftstore"
)

func TestNodeCrashBetweenLogAndDatabase(t *testing.T) {
	for _, phase := range []string{"uncommitted", "committed", "applied"} {
		t.Run(phase, func(t *testing.T) {
			f := newFixture(t, 1)
			f.start(t, 1)
			f.close(t)
			child := exec.Command(os.Args[0], "-test.run=^TestNodeCrashChild$")
			child.Env = append(os.Environ(), "GODB_NODE_CRASH_DIR="+filepath.Dir(f.dbPath), "GODB_NODE_CRASH_PHASE="+phase)
			output, err := child.CombinedOutput()

			var exited *exec.ExitError

			require.ErrorAs(t, err, &exited, "%s", output)
			require.Equal(t, 77, exited.ExitCode(), "%s", output)
			f.reopen(t)

			if phase == "uncommitted" {
				_, found, err := f.db.Get([]byte("key"))
				require.NoError(t, err)
				require.False(t, found)
				_, err = f.node.Campaign()
				require.NoError(t, err)
			}

			assertValue(t, f.db, "key", "value")
			result, found, err := f.db.AppliedRequest(uuid.UUID{9})
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, database.CommandOK, result.Code)

			if phase == "applied" {
				require.Zero(t, f.db.Stats().Writes)
			}

			f.close(t)
			require.NoError(t, database.Validate(f.dbPath))
		})
	}
}

func TestNodeCrashChild(t *testing.T) {
	dir := os.Getenv("GODB_NODE_CRASH_DIR")
	if dir == "" {
		t.Skip("crash subprocess only")
	}

	db, err := database.Open(filepath.Join(dir, "data.db"))
	require.NoError(t, err)
	store, err := raftstore.Open(filepath.Join(dir, "node.raft"), raftstore.Identity{GroupID: uuid.UUID{1}, NodeID: 1})
	require.NoError(t, err)
	logged := &hookedStorage{Storage: store}
	applied := &hookedDatabase{StateMachine: db}
	node, _, err := New(Config{Store: logged, Database: applied})
	require.NoError(t, err)
	_, err = node.Campaign()
	require.NoError(t, err)
	status, err := node.Status()
	require.NoError(t, err)
	phase := os.Getenv("GODB_NODE_CRASH_PHASE")
	logged.save = func(ready raft.Ready) error {
		require.NoError(t, store.Save(ready))

		if phase == "uncommitted" && len(ready.Entries) != 0 {
			os.Exit(77)
		}

		if phase == "committed" && ready.GetCommit() > status.Commit {
			os.Exit(77)
		}

		return nil
	}
	applied.apply = func(entry database.LogEntry) (database.CommandResult, error) {
		result, err := db.ApplyLogEntry(entry)
		require.NoError(t, err)

		if phase == "applied" && entry.RequestID != uuid.Nil() {
			os.Exit(77)
		}

		return result, nil
	}
	_, err = node.Propose(uuid.UUID{9}, commandPut(t, "key", "value"))
	require.NoError(t, err)
	t.Fatal("crash hook was not reached")
}

func TestNodeStopsOnMalformedCommittedEntry(t *testing.T) {
	f := newFixture(t, 1)
	f.start(t, 1)
	f.node.Close()
	require.NoError(t, f.store.Save(raft.Ready{
		HardState: &raftpb.HardState{Term: proto.Uint64(1), Commit: proto.Uint64(2)},
		Entries:   []*raftpb.Entry{{Index: proto.Uint64(2), Term: proto.Uint64(1), Data: []byte("invalid proposal")}},
	}))
	_, output, err := New(Config{Store: f.store, Database: f.db})
	require.ErrorIs(t, err, ErrStopped)
	require.ErrorIs(t, err, ErrInvalidProposal)
	require.Empty(t, output)
	position, err := f.db.AppliedPosition()
	require.NoError(t, err)
	require.Equal(t, uint64(1), position.Index)
}
