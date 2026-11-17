package server

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestMySQLNullPredicates(t *testing.T) {
	c := setupCluster(t, 1)
	enableMySQL(t, c, 1)
	c.start(t, 1)
	c.leader(t)

	db := mysqlClient(t, c, 1, "godb", "test-password", "godb")
	for _, query := range []string{
		"CREATE TABLE t(id BIGINT PRIMARY KEY,token TEXT UNIQUE,tenant BIGINT,active BOOL,UNIQUE pair(tenant,token))",
		"INSERT INTO t(id,tenant,active) VALUES(1,7,TRUE)",
		"INSERT INTO t(id,tenant,active) VALUES(2,7,FALSE)",
		"INSERT INTO t(id,tenant,token) VALUES(3,7,'x')",
	} {
		_, err := db.Exec(query)
		require.NoError(t, err)
	}

	before, err := c.members[1].db.AppliedPosition()
	require.NoError(t, err)
	rows, err := db.Query("SELECT id FROM t WHERE token IS NULL ORDER BY id")
	require.NoError(t, err)
	_, values := readDiscoveryRows(t, rows)
	require.Equal(t, [][]any{{"1"}, {"2"}}, values)
	statement, err := db.Prepare("SELECT id FROM t WHERE token IS NULL AND tenant=? AND active IS NOT NULL ORDER BY id DESC LIMIT ? OFFSET ?")
	require.NoError(t, err)
	defer func() { require.NoError(t, statement.Close()) }()
	rows, err = statement.Query(int64(7), int64(1), int64(1))
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Equal(t, [][]any{{"1"}}, values)
	rows, err = statement.Query(nil, int64(1), int64(0))
	require.NoError(t, err)
	columns, values := readDiscoveryRows(t, rows)
	require.Equal(t, []string{"id"}, columns)
	require.Empty(t, values)
	nullCount, err := db.Prepare("SELECT COUNT(*) AS aggregate FROM t WHERE token IS NULL")
	require.NoError(t, err)
	defer func() { require.NoError(t, nullCount.Close()) }()

	var count int64

	require.NoError(t, nullCount.QueryRow().Scan(&count))
	require.Equal(t, int64(2), count)
	require.NoError(t, db.QueryRow("SELECT COUNT(token) FROM t WHERE token IS NULL").Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM t WHERE token IS NOT NULL").Scan(&count))
	require.Equal(t, int64(1), count)
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM t WHERE token=NULL").Scan(&count))
	require.Zero(t, count)

	for _, query := range []string{"SELECT id FROM t WHERE token IS ?", "SELECT id FROM t WHERE token IS NOT ?", "SELECT id FROM t WHERE missing IS NULL", "DELETE FROM t WHERE id IS NULL"} {
		_, err := db.Prepare(query)
		require.Error(t, err, query)
	}

	after, err := c.members[1].db.AppliedPosition()
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, float64(6), testutil.ToFloat64(c.members[1].metrics.sqlSelects.WithLabelValues("index_scan", "ok")))
	require.Equal(t, float64(1), testutil.ToFloat64(c.members[1].metrics.sqlSelects.WithLabelValues("table_scan", "ok")))
	require.Equal(t, float64(7), testutil.ToFloat64(c.members[1].metrics.sqlRows))
	_, err = db.Exec("UPDATE t SET token='changed' WHERE id=1")
	require.NoError(t, err)
	require.NoError(t, nullCount.QueryRow().Scan(&count))
	require.Equal(t, int64(1), count)
	require.NoError(t, db.Ping())
}
