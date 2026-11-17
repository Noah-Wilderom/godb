package server

import (
	"testing"

	driver "github.com/go-sql-driver/mysql"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestMySQLExplainTextAndPrepared(t *testing.T) {
	c := setupCluster(t, 1)
	enableMySQL(t, c, 1)
	c.start(t, 1)
	c.leader(t)
	db := mysqlClient(t, c, 1, "godb", "test-password", "godb")
	_, err := db.Exec("CREATE TABLE t(id BIGINT PRIMARY KEY,tenant BIGINT,score BIGINT,INDEX by_score(tenant,score))")
	require.NoError(t, err)
	before, err := c.members[1].db.AppliedPosition()
	require.NoError(t, err)
	rows, err := db.Query("EXPLAIN SELECT id FROM t")
	require.NoError(t, err)
	types, err := rows.ColumnTypes()
	require.NoError(t, err)
	require.Len(t, types, 11)
	nullable, known := types[2].Nullable()
	require.True(t, known)
	require.True(t, nullable)
	require.Equal(t, "BIGINT", types[7].DatabaseTypeName())
	columns, values := readDiscoveryRows(t, rows)
	require.Equal(t, []string{"table", "access", "index", "equality_prefix_columns", "range_column", "lower_bound", "upper_bound", "candidate_limit", "result_limit", "result_offset", "extra"}, columns)
	require.Equal(t, [][]any{{"t", "table_scan", nil, "0", nil, nil, nil, "1024", "1024", "0", ""}}, values)
	statement, err := db.Prepare("EXPLAIN SELECT id FROM t WHERE tenant=? AND score>=? ORDER BY score LIMIT ? OFFSET ?")
	require.NoError(t, err)
	defer func() { require.NoError(t, statement.Close()) }()
	rows, err = statement.Query(int64(7), int64(50), int64(0), int64(1))
	require.NoError(t, err)
	again, values := readDiscoveryRows(t, rows)
	require.Equal(t, columns, again)
	require.Equal(t, [][]any{{"t", "index_range", "by_score", "1", "score", "inclusive", "unbounded", "1024", "0", "1", "Recheck 2 predicates; Sort captured rows"}}, values)
	rows, err = statement.Query(nil, int64(50), int64(0), int64(1))
	require.NoError(t, err)
	again, values = readDiscoveryRows(t, rows)
	require.Equal(t, columns, again)
	require.Len(t, values, 1)
	require.Equal(t, "0", values[0][7])
	rows, err = db.Query("EXPLAIN SELECT COUNT(*) FROM t LIMIT 0")
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Len(t, values, 1)
	require.Equal(t, "COUNT(*)", values[0][10])

	var wire *driver.MySQLError

	for _, query := range []string{"EXPLAIN ANALYZE SELECT id FROM t", "EXPLAIN UPDATE t SET score=1 WHERE id=1"} {
		_, err := db.Query(query)
		require.ErrorAs(t, err, &wire)
		require.Equal(t, uint16(1064), wire.Number)
	}

	_, err = db.Prepare("EXPLAIN SELECT missing FROM t")
	require.ErrorAs(t, err, &wire)
	require.Equal(t, uint16(1054), wire.Number)
	after, err := c.members[1].db.AppliedPosition()
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Zero(t, testutil.CollectAndCount(c.members[1].metrics.sqlSelects))
	require.Zero(t, testutil.CollectAndCount(c.members[1].metrics.sqlAggregates))
	require.Equal(t, float64(4), testutil.ToFloat64(c.members[1].metrics.sqlRows))
	require.NoError(t, db.Ping())
}
