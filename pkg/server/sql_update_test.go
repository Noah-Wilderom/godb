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

func TestSQLUpdateReplicationAndRetry(t *testing.T) {
	c := setupCluster(t, 3)
	for id := uint64(1); id <= 3; id++ {
		c.start(t, id)
	}

	leader := c.leader(t)
	s := c.members[leader]
	executeSQL(t, s, uuid.NewV4(), prepareSQL(t, s, "CREATE TABLE t(id BIGINT PRIMARY KEY,name TEXT UNIQUE,note TEXT)"))
	executeSQL(t, s, uuid.NewV4(), prepareSQL(t, s, "INSERT INTO t(id,name,note) VALUES(1,'before','original')"))
	rename := prepareSQL(t, s, "UPDATE t SET name='after' WHERE id=1")
	note := prepareSQL(t, s, "UPDATE t SET note='preserved' WHERE id=1")
	renameID := uuid.NewV4()
	require.Equal(t, sql.Result{AffectedRows: 1, MatchedRows: 1}, executeSQL(t, s, renameID, rename))
	require.Equal(t, sql.Result{AffectedRows: 1, MatchedRows: 1}, executeSQL(t, s, uuid.NewV4(), note))
	noopID := uuid.NewV4()
	require.Equal(t, sql.Result{MatchedRows: 1}, executeSQL(t, s, noopID, note))
	missing := prepareSQL(t, s, "UPDATE t SET note='should-not-retry' WHERE id=99")
	missingID := uuid.NewV4()
	require.Equal(t, sql.Result{}, executeSQL(t, s, missingID, missing))
	executeSQL(t, s, uuid.NewV4(), prepareSQL(t, s, "UPDATE t SET name='latest',note='latest' WHERE id=1"))
	executeSQL(t, s, uuid.NewV4(), prepareSQL(t, s, "INSERT INTO t(id,name,note) VALUES(99,'new','new')"))
	follower := c.members[leader%3+1]
	_, err := follower.ExecuteSQL(context.Background(), uuid.NewV4(), rename)
	require.ErrorIs(t, err, replication.ErrNotLeader)

	for _, member := range c.members {
		require.Eventually(t, func() bool {
			result, found, err := member.db.AppliedRequest(renameID)
			return err == nil && found && result.AffectedRows == 1 && result.MatchedRows == 1
		}, 5*time.Second, 10*time.Millisecond)
	}

	require.NoError(t, s.Close())
	next := c.members[c.leader(t)]
	require.Equal(t, sql.Result{AffectedRows: 1, MatchedRows: 1}, executeSQL(t, next, renameID, rename))
	require.Equal(t, sql.Result{MatchedRows: 1}, executeSQL(t, next, noopID, note))
	require.Equal(t, sql.Result{}, executeSQL(t, next, missingID, missing))
	read := prepareSQL(t, next, "SELECT name,note FROM t WHERE id=1")
	latest, err := record.NewText("latest")
	require.NoError(t, err)
	require.Equal(t, [][]record.Value{{latest, latest}}, executeSQL(t, next, uuid.Nil(), read).Rows)
	require.Len(t, executeSQL(t, next, uuid.Nil(), prepareSQL(t, next, "SELECT id FROM t WHERE name='latest'")).Rows, 1)
	require.Empty(t, executeSQL(t, next, uuid.Nil(), prepareSQL(t, next, "SELECT id FROM t WHERE name='after'")).Rows)
	newValue, err := record.NewText("new")
	require.NoError(t, err)
	require.Equal(t, [][]record.Value{{newValue}}, executeSQL(t, next, uuid.Nil(), prepareSQL(t, next, "SELECT note FROM t WHERE id=99")).Rows)
}
