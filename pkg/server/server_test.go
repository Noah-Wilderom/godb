package server

import (
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/internal/testutil"
	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/raftstore"
	"github.com/Noah-Wilderom/godb/pkg/replication"
)

type testCluster struct {
	configs   map[uint64]Config
	members   map[uint64]*Server
	listeners map[uint64]net.Listener
	ca        *testutil.Authority
}

func setupCluster(t testing.TB, count uint64) *testCluster {
	t.Helper()
	c := &testCluster{configs: make(map[uint64]Config), members: make(map[uint64]*Server), listeners: make(map[uint64]net.Listener), ca: testutil.NewAuthority(t)}
	peers := make(map[uint64]string)

	for id := uint64(1); id <= count; id++ {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		c.listeners[id] = listener
		peers[id] = listener.Addr().String()
	}

	for id := uint64(1); id <= count; id++ {
		dir := t.TempDir()
		group := uuid.UUID{1}
		cert, key := c.ca.Issue(t, group, id)
		config := Config{Identity: raftstore.Identity{GroupID: group, NodeID: id}, DatabasePath: filepath.Join(dir, "data.db"), RaftPath: filepath.Join(dir, "node.raft"), Peers: peers, Bootstrap: true, CertFile: cert, KeyFile: key, CAFile: c.ca.Path, TickInterval: 10 * time.Millisecond, RPCTimeout: 150 * time.Millisecond}
		require.NoError(t, database.Create(config.DatabasePath))
		c.configs[id] = config
	}

	t.Cleanup(func() {
		for _, member := range c.members {
			require.NoError(t, member.Close())
		}

		for _, listener := range c.listeners {
			_ = listener.Close()
		}
	})
	return c
}

func (c *testCluster) start(t testing.TB, id uint64) {
	t.Helper()
	member, err := Start(context.Background(), c.configs[id], c.listeners[id])
	require.NoError(t, err)
	c.members[id] = member
}

func (c *testCluster) leader(t *testing.T) uint64 {
	t.Helper()

	var leader uint64

	require.Eventually(t, func() bool {
		leader = 0

		for id, member := range c.members {
			state, err := member.Status()
			if err == nil && state.IsLeader {
				if leader != 0 {
					return false
				}

				leader = id
			}
		}

		return leader != 0
	}, 10*time.Second, 10*time.Millisecond)
	return leader
}

func putCommand(t testing.TB, key, value string) []byte {
	t.Helper()
	command, err := database.EncodeCommand(database.Command{Operations: []database.CommandOperation{{Kind: database.CommandPut, Key: []byte(key), Value: []byte(value)}}})
	require.NoError(t, err)
	return command
}

func propose(t testing.TB, member *Server, id uuid.UUID, command []byte) replication.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := member.Propose(ctx, id, command)
	require.NoError(t, err)
	return result
}

func waitValue(t *testing.T, member *Server, key, expected string) {
	t.Helper()
	require.Eventually(t, func() bool {
		value, found, err := member.db.Get([]byte(key))
		return err == nil && found && string(value) == expected
	}, 5*time.Second, 10*time.Millisecond)
}

func TestServerReplicatesOverTLSAndRestartsMember(t *testing.T) {
	c := setupCluster(t, 3)
	for id := uint64(1); id <= 3; id++ {
		c.start(t, id)
	}

	leader := c.leader(t)
	id := uuid.UUID{2}
	command := putCommand(t, "key", "one")
	result := propose(t, c.members[leader], id, command)
	require.Equal(t, database.CommandOK, result.Command.Code)
	require.Equal(t, sha256.Sum256(command), result.Digest)

	for _, member := range c.members {
		waitValue(t, member, "key", "one")
	}

	propose(t, c.members[leader], uuid.UUID{3}, putCommand(t, "key", "two"))
	result = propose(t, c.members[leader], id, command)
	require.Equal(t, database.CommandOK, result.Command.Code)
	result = propose(t, c.members[leader], id, putCommand(t, "key", "conflict"))
	require.Equal(t, database.CommandRequestIDConflict, result.Command.Code)

	for _, member := range c.members {
		waitValue(t, member, "key", "two")
	}

	follower := leader%3 + 1
	_, err := c.members[follower].Propose(context.Background(), uuid.UUID{4}, command)
	require.ErrorIs(t, err, replication.ErrNotLeader)
	require.NoError(t, c.members[follower].Close())
	propose(t, c.members[leader], uuid.UUID{5}, putCommand(t, "key", "while offline"))
	listener, err := net.Listen("tcp", c.configs[follower].Peers[follower])
	require.NoError(t, err)
	c.listeners[follower] = listener
	config := c.configs[follower]
	config.Bootstrap = false
	c.configs[follower] = config
	c.start(t, follower)
	waitValue(t, c.members[follower], "key", "while offline")

	for id, member := range c.members {
		require.Greater(t, member.TransportStats().Sent, uint64(0))
		require.NoError(t, member.Close())
		require.NoError(t, database.Validate(c.configs[id].DatabasePath))
	}
}

func TestServerLeaderLossAndNoQuorum(t *testing.T) {
	c := setupCluster(t, 3)
	for id := uint64(1); id <= 3; id++ {
		c.start(t, id)
	}

	leader := c.leader(t)
	propose(t, c.members[leader], uuid.UUID{2}, putCommand(t, "key", "before"))
	require.NoError(t, c.members[leader].Close())
	next := c.leader(t)
	require.NotEqual(t, leader, next)
	propose(t, c.members[next], uuid.UUID{3}, putCommand(t, "key", "after"))

	for id, member := range c.members {
		if id != next {
			require.NoError(t, member.Close())
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	result, err := c.members[next].Propose(ctx, uuid.UUID{4}, putCommand(t, "key", "no quorum"))
	require.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, replication.ErrNotLeader), "%v", err)
	require.Zero(t, result)
	value, found, err := c.members[next].db.Get([]byte("key"))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "after", string(value))
}

func TestServerCancellationShutdownAndUnlock(t *testing.T) {
	c := setupCluster(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	member, err := Start(ctx, c.configs[1], c.listeners[1])
	require.NoError(t, err)
	c.members[1] = member
	c.leader(t)
	requestCtx, stop := context.WithCancel(context.Background())
	stop()
	_, err = member.Propose(requestCtx, uuid.UUID{2}, putCommand(t, "key", "value"))
	require.ErrorIs(t, err, context.Canceled)
	cancel()
	require.NoError(t, member.Wait())
	require.NoError(t, member.Close())
	_, err = member.Propose(context.Background(), uuid.UUID{2}, putCommand(t, "key", "value"))
	require.ErrorIs(t, err, ErrClosed)
	db, err := database.Open(c.configs[1].DatabasePath)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	store, err := raftstore.Open(c.configs[1].RaftPath, c.configs[1].Identity)
	require.NoError(t, err)
	require.NoError(t, store.Close())
}

func TestServerMatchesPendingResultsByCommandDigest(t *testing.T) {
	ctx := context.Background()
	id := uuid.UUID{1}
	command := []byte("new command")
	request := &proposal{ctx: ctx, id: id, command: command, digest: sha256.Sum256(command), reply: make(chan proposalResult, 1)}
	pending := map[uuid.UUID]*proposal{id: request}
	s := &Server{}
	s.dispatch(replication.Output{Results: []replication.Result{{RequestID: id, Digest: sha256.Sum256([]byte("old command"))}}}, pending)
	require.Len(t, pending, 1)
	require.Empty(t, request.reply)
	result := replication.Result{RequestID: id, Digest: request.digest, Command: database.CommandResult{Code: database.CommandRequestIDConflict}}
	s.dispatch(replication.Output{Results: []replication.Result{result}}, pending)
	require.Empty(t, pending)
	require.Equal(t, result, (<-request.reply).result)
}

func TestServerApplyFailureStopsAndCanRecover(t *testing.T) {
	c := setupCluster(t, 1)
	c.start(t, 1)
	c.leader(t)
	member := c.members[1]
	require.NoError(t, member.db.Close())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id := uuid.UUID{2}
	command := putCommand(t, "key", "value")
	_, err := member.Propose(ctx, id, command)
	require.True(t, errors.Is(err, replication.ErrStopped) || errors.Is(err, ErrClosed), "%v", err)
	require.ErrorIs(t, member.Wait(), replication.ErrStopped)
	delete(c.members, 1)
	listener, err := net.Listen("tcp", c.configs[1].Peers[1])
	require.NoError(t, err)
	c.listeners[1] = listener
	c.start(t, 1)
	c.leader(t)
	result := propose(t, c.members[1], id, command)
	require.Equal(t, database.CommandOK, result.Command.Code)
	waitValue(t, c.members[1], "key", "value")
}
