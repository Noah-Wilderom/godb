package server

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/replication"
)

func TestSQLTableScanLeaderChangeAndQuorum(t *testing.T) {
	c := setupCluster(t, 3)
	for id := uint64(1); id <= 3; id++ {
		c.start(t, id)
	}

	leader := c.leader(t)
	s := c.members[leader]
	executeSQL(t, s, uuid.NewV4(), prepareSQL(t, s, "CREATE TABLE t(id BIGINT PRIMARY KEY,active BOOL)"))
	executeSQL(t, s, uuid.NewV4(), prepareSQL(t, s, "INSERT INTO t(id,active) VALUES(1,TRUE)"))
	scan := prepareSQL(t, s, "SELECT id FROM t ORDER BY id DESC LIMIT 1")
	filtered := prepareSQL(t, s, "SELECT id FROM t WHERE active=TRUE ORDER BY id DESC LIMIT 1")
	zero := prepareSQL(t, s, "SELECT id FROM t LIMIT 0")
	before, err := s.db.AppliedPosition()
	require.NoError(t, err)
	require.Equal(t, [][]record.Value{{record.NewInt64(1)}}, executeSQL(t, s, uuid.Nil(), scan).Rows)
	after, err := s.db.AppliedPosition()
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, err = c.members[leader%3+1].ExecuteSQL(context.Background(), uuid.Nil(), scan)
	require.ErrorIs(t, err, replication.ErrNotLeader)
	require.NoError(t, s.Close())
	next := c.members[c.leader(t)]
	executeSQL(t, next, uuid.NewV4(), prepareSQL(t, next, "INSERT INTO t(id,active) VALUES(2,TRUE)"))

	for _, plan := range []*SQLPlan{scan, filtered} {
		require.Equal(t, [][]record.Value{{record.NewInt64(2)}}, executeSQL(t, next, uuid.Nil(), plan).Rows)
	}

	for _, member := range c.members {
		if member != next {
			require.NoError(t, member.Close())
		}
	}

	for _, plan := range []*SQLPlan{scan, filtered, zero} {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		result, err := next.ExecuteSQL(ctx, uuid.Nil(), plan)
		cancel()
		require.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, replication.ErrNotLeader), "%v", err)
		require.Empty(t, result.Rows)
	}

	failures := testutil.ToFloat64(next.metrics.sqlSelects.WithLabelValues("table_scan", "error")) + testutil.ToFloat64(next.metrics.sqlSelects.WithLabelValues("table_scan", "canceled"))
	require.GreaterOrEqual(t, failures, float64(3))
}
