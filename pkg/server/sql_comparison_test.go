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

func TestSQLComparisonLeaderChangeAndQuorum(t *testing.T) {
	c := setupCluster(t, 3)
	for id := uint64(1); id <= 3; id++ {
		c.start(t, id)
	}

	leader := c.leader(t)
	s := c.members[leader]
	executeSQL(t, s, uuid.NewV4(), prepareSQL(t, s, "CREATE TABLE t(id BIGINT PRIMARY KEY,score BIGINT,bucket BIGINT,INDEX by_bucket(bucket,score))"))
	executeSQL(t, s, uuid.NewV4(), prepareSQL(t, s, "INSERT INTO t(id,score,bucket) VALUES(1,10,1)"))
	scan := prepareSQL(t, s, "SELECT id FROM t WHERE id>0 AND score>=10 ORDER BY id")
	count := prepareSQL(t, s, "SELECT COUNT(*) FROM t WHERE bucket=1 AND score>10")
	null := prepareSQL(t, s, "SELECT COUNT(*) FROM t WHERE score<=NULL")
	zero := prepareSQL(t, s, "SELECT id FROM t WHERE id>0 LIMIT 0")
	secondary := prepareSQL(t, s, "SELECT id FROM t WHERE bucket=1 AND score>=10 ORDER BY id")
	emptySecondary := prepareSQL(t, s, "SELECT COUNT(*) FROM t WHERE bucket=1 AND score>20 AND score<0")
	empty := prepareSQL(t, s, "SELECT COUNT(*) FROM t WHERE id>10 AND id<1")
	require.Equal(t, [][]record.Value{{record.NewInt64(1)}}, executeSQL(t, s, uuid.Nil(), scan).Rows)
	_, err := c.members[leader%3+1].ExecuteSQL(context.Background(), uuid.Nil(), scan)
	require.ErrorIs(t, err, replication.ErrNotLeader)
	require.NoError(t, s.Close())
	next := c.members[c.leader(t)]
	executeSQL(t, next, uuid.NewV4(), prepareSQL(t, next, "INSERT INTO t(id,score,bucket) VALUES(2,20,1)"))
	require.Equal(t, [][]record.Value{{record.NewInt64(1)}, {record.NewInt64(2)}}, executeSQL(t, next, uuid.Nil(), scan).Rows)
	require.Equal(t, [][]record.Value{{record.NewInt64(1)}}, executeSQL(t, next, uuid.Nil(), count).Rows)
	require.Equal(t, [][]record.Value{{record.NewInt64(1)}, {record.NewInt64(2)}}, executeSQL(t, next, uuid.Nil(), secondary).Rows)
	executeSQL(t, next, uuid.NewV4(), prepareSQL(t, next, "UPDATE t SET score=0 WHERE id=1"))
	require.Equal(t, [][]record.Value{{record.NewInt64(2)}}, executeSQL(t, next, uuid.Nil(), scan).Rows)

	require.Equal(t, [][]record.Value{{record.NewInt64(0)}}, executeSQL(t, next, uuid.Nil(), empty).Rows)
	require.Equal(t, [][]record.Value{{record.NewInt64(2)}}, executeSQL(t, next, uuid.Nil(), secondary).Rows)
	require.Equal(t, [][]record.Value{{record.NewInt64(0)}}, executeSQL(t, next, uuid.Nil(), emptySecondary).Rows)

	for _, member := range c.members {
		if member != next {
			require.NoError(t, member.Close())
		}
	}

	for _, plan := range []*SQLPlan{scan, count, null, zero, empty, secondary, emptySecondary} {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		result, err := next.ExecuteSQL(ctx, uuid.Nil(), plan)
		cancel()
		require.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, replication.ErrNotLeader), "%v", err)
		require.Empty(t, result.Rows)
	}
}
