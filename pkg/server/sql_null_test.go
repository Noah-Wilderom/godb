package server

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/replication"
)

func TestSQLNullPredicatesLeaderChangeAndQuorum(t *testing.T) {
	c := setupCluster(t, 3)
	for id := uint64(1); id <= 3; id++ {
		c.start(t, id)
	}

	leader := c.leader(t)
	s := c.members[leader]
	executeSQL(t, s, uuid.NewV4(), prepareSQL(t, s, "CREATE TABLE t(id BIGINT PRIMARY KEY,token TEXT UNIQUE)"))
	executeSQL(t, s, uuid.NewV4(), prepareSQL(t, s, "INSERT INTO t(id) VALUES(1)"))
	count := prepareSQL(t, s, "SELECT COUNT(*) FROM t WHERE token IS NULL")
	read := prepareSQL(t, s, "SELECT id FROM t WHERE token IS NULL ORDER BY id")
	impossible := prepareSQL(t, s, "SELECT COUNT(*) FROM t WHERE id IS NULL")
	nonnull := prepareSQL(t, s, "SELECT COUNT(*) FROM t WHERE token IS NOT NULL")
	require.Equal(t, [][]record.Value{{record.NewInt64(1)}}, executeSQL(t, s, uuid.Nil(), count).Rows)
	_, err := c.members[leader%3+1].ExecuteSQL(context.Background(), uuid.Nil(), read)
	require.ErrorIs(t, err, replication.ErrNotLeader)
	require.NoError(t, s.Close())
	next := c.members[c.leader(t)]
	executeSQL(t, next, uuid.NewV4(), prepareSQL(t, next, "INSERT INTO t(id) VALUES(2)"))
	require.Equal(t, [][]record.Value{{record.NewInt64(1)}, {record.NewInt64(2)}}, executeSQL(t, next, uuid.Nil(), read).Rows)
	require.Equal(t, [][]record.Value{{record.NewInt64(2)}}, executeSQL(t, next, uuid.Nil(), count).Rows)
	executeSQL(t, next, uuid.NewV4(), prepareSQL(t, next, "UPDATE t SET token='changed' WHERE id=1"))
	require.Equal(t, [][]record.Value{{record.NewInt64(2)}}, executeSQL(t, next, uuid.Nil(), read).Rows)
	require.Equal(t, [][]record.Value{{record.NewInt64(1)}}, executeSQL(t, next, uuid.Nil(), nonnull).Rows)

	for _, member := range c.members {
		if member != next {
			require.NoError(t, member.Close())
		}
	}

	for _, plan := range []*SQLPlan{read, count, impossible, nonnull} {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		result, err := next.ExecuteSQL(ctx, uuid.Nil(), plan)
		cancel()
		require.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, replication.ErrNotLeader), "%v", err)
		require.Empty(t, result.Rows)
	}
}
