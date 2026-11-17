package server

import (
	"context"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/replication"
	"github.com/Noah-Wilderom/godb/pkg/sql"
)

func TestSQLDeleteReplicationAndRetry(t *testing.T) {
	c := setupCluster(t, 3)
	for id := uint64(1); id <= 3; id++ {
		c.start(t, id)
	}

	leader := c.leader(t)
	s := c.members[leader]
	executeSQL(t, s, uuid.NewV4(), prepareSQL(t, s, "CREATE TABLE t(id BIGINT PRIMARY KEY,name TEXT UNIQUE)"))
	executeSQL(t, s, uuid.NewV4(), prepareSQL(t, s, "INSERT INTO t(id,name) VALUES(1,'before')"))
	deletion := prepareSQL(t, s, "DELETE FROM t WHERE id=1")
	_, err := c.members[leader%3+1].ExecuteSQL(context.Background(), uuid.NewV4(), deletion)
	require.ErrorIs(t, err, replication.ErrNotLeader)
	deletedID, missingID := uuid.NewV4(), uuid.NewV4()
	require.Equal(t, sql.Result{AffectedRows: 1, MatchedRows: 1}, executeSQL(t, s, deletedID, deletion))
	require.Equal(t, sql.Result{}, executeSQL(t, s, missingID, deletion))
	executeSQL(t, s, uuid.NewV4(), prepareSQL(t, s, "INSERT INTO t(id,name) VALUES(1,'replacement')"))

	for _, member := range c.members {
		require.Eventually(t, func() bool {
			removed, found, err := member.db.AppliedRequest(deletedID)
			if err != nil || !found || removed.AffectedRows != 1 || removed.MatchedRows != 1 {
				return false
			}

			missing, found, err := member.db.AppliedRequest(missingID)
			return err == nil && found && missing.AffectedRows == 0 && missing.MatchedRows == 0
		}, 5*time.Second, 10*time.Millisecond)
	}

	require.NoError(t, s.Close())
	next := c.members[c.leader(t)]
	require.Equal(t, sql.Result{AffectedRows: 1, MatchedRows: 1}, executeSQL(t, next, deletedID, deletion))
	require.Equal(t, sql.Result{}, executeSQL(t, next, missingID, deletion))
	read := prepareSQL(t, next, "SELECT id FROM t WHERE name='replacement'")
	require.Equal(t, [][]record.Value{{record.NewInt64(1)}}, executeSQL(t, next, uuid.Nil(), read).Rows)
	require.Empty(t, executeSQL(t, next, uuid.Nil(), prepareSQL(t, next, "SELECT id FROM t WHERE name='before'")).Rows)
	require.Equal(t, sql.Result{AffectedRows: 1, MatchedRows: 1}, executeSQL(t, next, uuid.NewV4(), deletion))
	require.Empty(t, executeSQL(t, next, uuid.Nil(), read).Rows)
}
