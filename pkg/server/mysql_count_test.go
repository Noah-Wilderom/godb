package server

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestMySQLCountTextAndPrepared(t *testing.T) {
	c := setupCluster(t, 1)
	enableMySQL(t, c, 1)
	c.start(t, 1)
	c.leader(t)
	db := mysqlClient(t, c, 1, "godb", "test-password", "godb")
	_, err := db.Exec("CREATE TABLE t(id BIGINT PRIMARY KEY,name TEXT,active BOOL)")
	require.NoError(t, err)

	var count int64

	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM t").Scan(&count))
	require.Zero(t, count)

	for _, query := range []string{"INSERT INTO t(id,name,active) VALUES(1,'',FALSE)", "INSERT INTO t(id,name,active) VALUES(2,'a',TRUE)", "INSERT INTO t(id) VALUES(3)"} {
		_, err := db.Exec(query)
		require.NoError(t, err)
	}

	before, err := c.members[1].db.AppliedPosition()
	require.NoError(t, err)
	rows, err := db.Query("SELECT COUNT(*) AS aggregate FROM t")
	require.NoError(t, err)
	types, err := rows.ColumnTypes()
	require.NoError(t, err)
	require.Len(t, types, 1)
	require.Equal(t, "BIGINT", types[0].DatabaseTypeName())
	nullable, known := types[0].Nullable()
	require.True(t, known)
	require.False(t, nullable)
	columns, values := readDiscoveryRows(t, rows)
	require.Equal(t, []string{"aggregate"}, columns)
	require.Equal(t, [][]any{{"3"}}, values)
	statement, err := db.Prepare("SELECT COUNT(name) AS `total names` FROM t WHERE active=? LIMIT ? OFFSET ?")
	require.NoError(t, err)
	defer func() { require.NoError(t, statement.Close()) }()

	for _, test := range []struct {
		active any
		want   int64
	}{{true, 1}, {false, 1}, {nil, 0}} {
		require.NoError(t, statement.QueryRow(test.active, int64(1), int64(0)).Scan(&count))
		require.Equal(t, test.want, count)
	}

	rows, err = statement.Query(true, int64(1), int64(1))
	require.NoError(t, err)
	columns, values = readDiscoveryRows(t, rows)
	require.Equal(t, []string{"total names"}, columns)
	require.Empty(t, values)
	plain, err := db.Prepare("SELECT COUNT(name) FROM t")
	require.NoError(t, err)
	require.NoError(t, plain.QueryRow().Scan(&count))
	require.Equal(t, int64(2), count)
	require.NoError(t, plain.Close())

	for _, query := range []string{"SELECT COUNT(missing) FROM t", "SELECT COUNT(*) FROM t ORDER BY id", "SELECT COUNT(DISTINCT name) FROM t", "SELECT COUNT(*),id FROM t", "SELECT COUNT(*) FROM t GROUP BY active"} {
		_, err := db.Query(query)
		require.Error(t, err, query)
	}

	after, err := c.members[1].db.AppliedPosition()
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, float64(7), testutil.ToFloat64(c.members[1].metrics.sqlAggregates.WithLabelValues("count", "ok")))
	require.Equal(t, float64(6), testutil.ToFloat64(c.members[1].metrics.sqlRows))
	require.NoError(t, db.Ping())
}
