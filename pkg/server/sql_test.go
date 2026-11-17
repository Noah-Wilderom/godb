package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/replication"
	"github.com/Noah-Wilderom/godb/pkg/sql"
)

func prepareSQL(t testing.TB, s *Server, query string, args ...record.Value) *SQLPlan {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	plan, err := s.PrepareSQL(ctx, query, args...)
	require.NoError(t, err)
	return plan
}

func executeSQL(t testing.TB, s *Server, id uuid.UUID, plan *SQLPlan) sql.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := s.ExecuteSQL(ctx, id, plan)
	require.NoError(t, err)
	return result
}

func TestSQLReplicationLeaderChangeAndRetry(t *testing.T) {
	c := setupCluster(t, 3)
	for id := uint64(1); id <= 3; id++ {
		c.start(t, id)
	}

	leader := c.leader(t)
	s := c.members[leader]
	create := prepareSQL(t, s, "CREATE TABLE users (id BIGINT PRIMARY KEY, name TEXT NOT NULL UNIQUE, tenant BIGINT, INDEX name_lookup(name), UNIQUE(tenant,name))")
	require.Equal(t, sql.CreateTable, create.Kind())
	executeSQL(t, s, uuid.UUID{10}, create)
	name, err := record.NewText("private-name")
	require.NoError(t, err)
	insert := prepareSQL(t, s, "INSERT INTO users (id,name,tenant) VALUES (?,?,7)", record.NewInt64(1), name)
	require.Equal(t, uint64(1), executeSQL(t, s, uuid.UUID{11}, insert).AffectedRows)
	executeSQL(t, s, uuid.UUID{15}, prepareSQL(t, s, "INSERT INTO users(id,name,tenant) VALUES(3,'z-sort',7)"))
	ordered := prepareSQL(t, s, "SELECT name FROM users WHERE tenant=? ORDER BY name DESC LIMIT ? OFFSET ?", record.NewInt64(7), record.NewInt64(1), record.NewInt64(1))
	require.Equal(t, [][]record.Value{{name}}, executeSQL(t, s, uuid.Nil(), ordered).Rows)
	read := prepareSQL(t, s, "SELECT name FROM users WHERE name=? AND tenant=7", name)
	require.Equal(t, [][]record.Value{{name}}, executeSQL(t, s, uuid.Nil(), read).Rows)
	follower := c.members[leader%3+1]
	_, err = follower.PrepareSQL(context.Background(), "SELECT name FROM users WHERE name=? AND tenant=7", name)
	require.ErrorIs(t, err, replication.ErrNotLeader)
	_, err = follower.ExecuteSQL(context.Background(), uuid.Nil(), read)
	require.ErrorIs(t, err, replication.ErrNotLeader)
	_, err = follower.ExecuteSQL(context.Background(), uuid.Nil(), ordered)
	require.ErrorIs(t, err, replication.ErrNotLeader)
	_, err = follower.ExecuteSQL(context.Background(), uuid.UUID{12}, insert)
	require.ErrorIs(t, err, replication.ErrNotLeader)

	for _, member := range c.members {
		require.Eventually(t, func() bool {
			table, found, err := member.db.GetTableByName("users")
			if err != nil || !found {
				return false
			}

			if len(table.Indexes) != 3 || table.Indexes[0].Name != "name" || table.Indexes[0].ID != 1 || !table.Indexes[0].Unique || table.Indexes[1].Name != "name_lookup" || table.Indexes[1].ID != 2 || table.Indexes[2].ID != 3 {
				return false
			}

			indexed, err := member.db.ScanRowsByIndex(table, 2, 10, name)
			if err != nil || len(indexed) != 1 {
				return false
			}

			row, found, err := member.db.GetRow(table, record.NewInt64(1))
			return err == nil && found && row[2] == name
		}, 5*time.Second, time.Millisecond*10)
	}

	require.NoError(t, s.Close())
	next := c.members[c.leader(t)]
	executeSQL(t, next, uuid.UUID{10}, create)
	executeSQL(t, next, uuid.UUID{11}, insert)
	require.Equal(t, [][]record.Value{{name}}, executeSQL(t, next, uuid.Nil(), read).Rows)
	require.Equal(t, [][]record.Value{{name}}, executeSQL(t, next, uuid.Nil(), ordered).Rows)
	command, err := insert.Command()
	require.NoError(t, err)
	command[0] ^= 0xff
	executeSQL(t, next, uuid.UUID{11}, insert)
	_, err = next.ExecuteSQL(context.Background(), uuid.UUID{12}, insert)

	var rejected *sql.CommandError

	require.ErrorAs(t, err, &rejected)
	require.Equal(t, database.CommandDuplicatePrimaryKey, rejected.Code)
	uniqueConflict := prepareSQL(t, next, "INSERT INTO users (id,name) VALUES (2,?)", name)
	_, err = next.ExecuteSQL(context.Background(), uuid.UUID{14}, uniqueConflict)
	require.ErrorAs(t, err, &rejected)
	require.Equal(t, database.CommandUniqueConflict, rejected.Code)
	other := prepareSQL(t, next, "INSERT INTO users (id,name) VALUES (2,'different')")
	_, err = next.ExecuteSQL(context.Background(), uuid.UUID{11}, other)
	require.ErrorAs(t, err, &rejected)
	require.Equal(t, database.CommandRequestIDConflict, rejected.Code)
	more := prepareSQL(t, next, "CREATE TABLE more (id BIGINT PRIMARY KEY)")
	executeSQL(t, next, uuid.UUID{13}, more)
	table, found, err := next.db.GetTableByName("more")
	require.NoError(t, err)
	require.True(t, found)
	require.NotEqual(t, uint32(1), table.ID)

	for _, member := range c.members {
		if member != next {
			require.NoError(t, member.Close())
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	result, err := next.ExecuteSQL(ctx, uuid.Nil(), ordered)
	require.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, replication.ErrNotLeader), "%v", err)
	require.Empty(t, result.Rows)
	_, err = next.PrepareSQL(ctx, "CREATE TABLE isolated (id BIGINT PRIMARY KEY)")
	require.Error(t, err)
}

func TestSQLConcurrentPreparationRestartAndMetrics(t *testing.T) {
	c := setupCluster(t, 1)
	c.start(t, 1)
	c.leader(t)
	s := c.members[1]
	plans := make([]*SQLPlan, 16)
	errs := make([]error, len(plans))

	var workers sync.WaitGroup

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for i := range plans {
		workers.Go(func() {
			plans[i], errs[i] = s.PrepareSQL(ctx, fmt.Sprintf("CREATE TABLE t%d (id BIGINT PRIMARY KEY)", i))
		})
	}

	workers.Wait()
	ids := make(map[uint32]bool)

	for i, plan := range plans {
		require.NoError(t, errs[i])
		executeSQL(t, s, uuid.UUID{byte(i + 1)}, plan)
		table, found, err := s.db.GetTableByName(fmt.Sprintf("t%d", i))
		require.NoError(t, err)
		require.True(t, found)
		require.False(t, ids[table.ID])
		ids[table.ID] = true
	}

	insert := prepareSQL(t, s, "INSERT INTO t0 (id) VALUES (999)")
	executeSQL(t, s, uuid.UUID{30}, insert)
	read := prepareSQL(t, s, "SELECT * FROM t0 WHERE id=999")
	executeSQL(t, s, uuid.Nil(), read)
	require.Equal(t, float64(1), testutil.ToFloat64(s.metrics.sqlRows))
	require.Equal(t, float64(1), testutil.ToFloat64(s.metrics.sqlAffected))
	require.Zero(t, testutil.ToFloat64(s.metrics.sqlActive))
	require.Zero(t, testutil.ToFloat64(s.metrics.readPending))
	registry := prometheus.NewPedanticRegistry()
	require.NoError(t, registry.Register(s.metrics.sqlDuration))
	_, err := registry.Gather()
	require.NoError(t, err)
	metrics, err := s.metrics.registry.Gather()
	require.NoError(t, err)

	for _, metric := range metrics {
		require.NotContains(t, metric.String(), "t0")
	}

	require.NoError(t, s.Close())
	listener, err := net.Listen("tcp", c.configs[1].Peers[1])
	require.NoError(t, err)
	c.listeners[1] = listener
	config := c.configs[1]
	config.Bootstrap = false
	c.configs[1] = config
	c.start(t, 1)
	c.leader(t)
	s = c.members[1]
	executeSQL(t, s, uuid.UUID{30}, insert)
	require.Len(t, executeSQL(t, s, uuid.Nil(), read).Rows, 1)
	plan := prepareSQL(t, s, "CREATE TABLE after_restart (id BIGINT PRIMARY KEY)")
	executeSQL(t, s, uuid.UUID{31}, plan)
	table, found, err := s.db.GetTableByName("after_restart")
	require.NoError(t, err)
	require.True(t, found)
	require.False(t, ids[table.ID])
}

func TestSQLInvalidRequestsAndCancellation(t *testing.T) {
	c := setupCluster(t, 1)
	c.start(t, 1)
	c.leader(t)
	s := c.members[1]
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.PrepareSQL(ctx, "CREATE TABLE canceled (id BIGINT PRIMARY KEY)")
	require.ErrorIs(t, err, context.Canceled)
	_, found, err := s.db.GetTableByName("canceled")
	require.NoError(t, err)
	require.False(t, found)
	_, err = s.PrepareSQL(context.Background(), "DROP DATABASE test")
	require.ErrorIs(t, err, sql.ErrSyntax)
	_, err = s.ExecuteSQL(context.Background(), uuid.Nil(), nil)
	require.ErrorIs(t, err, ErrSQLPlan)
	plan := prepareSQL(t, s, "CREATE TABLE good (id BIGINT PRIMARY KEY)")
	_, err = s.ExecuteSQL(context.Background(), uuid.Nil(), plan)
	require.ErrorIs(t, err, replication.ErrInvalidProposal)
	wrong := *plan
	wrong.group = uuid.UUID{255}
	_, err = s.ExecuteSQL(context.Background(), uuid.UUID{10}, &wrong)
	require.ErrorIs(t, err, ErrSQLPlan)
	_, err = s.ExecuteSQL(ctx, uuid.UUID{10}, plan)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, s.Close())
	_, err = s.PrepareSQL(context.Background(), "SELECT * FROM good WHERE id=1")
	require.ErrorIs(t, err, ErrClosed)
	_, err = s.ExecuteSQL(context.Background(), uuid.UUID{10}, plan)
	require.ErrorIs(t, err, ErrClosed)
	require.Zero(t, testutil.ToFloat64(s.metrics.sqlActive))
}

func TestReadCompletionWaitsForApplyAndRejectsOldTerm(t *testing.T) {
	c := setupCluster(t, 1)
	c.start(t, 1)
	c.leader(t)
	s := c.members[1]
	ran := 0
	newRequest := func() *readRequest {
		return &readRequest{ctx: context.Background(), term: 3, reply: make(chan readResponse, 1), run: func() (any, error) { ran++; return "row", nil }}
	}
	pending := map[uint64]*readRequest{1: newRequest()}
	request := pending[1]
	state := replication.Status{IsLeader: true, Term: 3, Applied: 8}
	s.completeReads([]replication.ReadResult{{ID: 1, Index: 9}, {ID: 99, Index: 9}}, state, pending)
	require.Zero(t, ran)
	require.Len(t, pending, 1)
	state.Applied = 9
	s.completeReads(nil, state, pending)
	require.Equal(t, 1, ran)
	require.Equal(t, "row", (<-request.reply).value)
	require.Empty(t, pending)
	pending[2] = newRequest()
	request = pending[2]
	state.Term = 4
	s.completeReads([]replication.ReadResult{{ID: 2, Index: 9}}, state, pending)
	require.Equal(t, 1, ran)
	require.ErrorIs(t, (<-request.reply).err, replication.ErrNotLeader)
	canceled, stop := context.WithCancel(context.Background())
	stop()
	request = newRequest()
	request.ctx = canceled
	pending[3] = request
	state.Term = 3
	s.completeReads(nil, state, pending)
	require.Len(t, pending, 1)
	require.Nil(t, request.run)
	s.completeReads([]replication.ReadResult{{ID: 3, Index: 9}}, state, pending)
	require.Empty(t, pending)
	require.ErrorIs(t, (<-request.reply).err, context.Canceled)
	require.Equal(t, 1, ran)
}

func TestSQLTableIDCollisionIsRejected(t *testing.T) {
	c := setupCluster(t, 1)
	c.start(t, 1)
	c.leader(t)
	s := c.members[1]
	plan := prepareSQL(t, s, "CREATE TABLE pending (id BIGINT PRIMARY KEY)")
	command, err := plan.Command()
	require.NoError(t, err)
	decoded, err := database.DecodeCommand(command)
	require.NoError(t, err)
	decoded.Operations[0].Table.Name = "external"
	command, err = database.EncodeCommand(decoded)
	require.NoError(t, err)
	propose(t, s, uuid.UUID{20}, command)
	_, err = s.ExecuteSQL(context.Background(), uuid.UUID{21}, plan)

	var rejected *sql.CommandError

	require.ErrorAs(t, err, &rejected)
	require.Equal(t, database.CommandTableIDExists, rejected.Code)
	_, found, err := s.db.GetTableByName("pending")
	require.NoError(t, err)
	require.False(t, found)
	require.False(t, strings.Contains(rejected.Error(), "pending"))
}

func TestReadAdmissionRemainsBoundedAfterCancellation(t *testing.T) {
	c := setupCluster(t, 1)
	c.start(t, 1)
	c.leader(t)
	s := c.members[1]
	state, err := s.Status()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	pending := make(map[uint64]*readRequest)
	for id := uint64(1); id <= maxPending; id++ {
		pending[id] = &readRequest{ctx: ctx, term: state.Term, reply: make(chan readResponse, 1)}
	}

	s.completeReads(nil, state, pending)
	require.Len(t, pending, maxPending)
	request := &readRequest{ctx: context.Background(), reply: make(chan readResponse, 1)}
	_, err = s.beginRead(request, pending)
	require.ErrorIs(t, err, ErrBusy)
	require.ErrorIs(t, (<-request.reply).err, ErrBusy)
	require.Len(t, pending, maxPending)
	state.Term++
	s.completeReads(nil, state, pending)
	require.Empty(t, pending)
}
