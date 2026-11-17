package server

import (
	"testing"

	driver "github.com/go-sql-driver/mysql"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/sql"
)

func TestMySQLTableScan(t *testing.T) {
	c := setupCluster(t, 1)
	enableMySQL(t, c, 1)
	c.start(t, 1)
	c.leader(t)

	db := mysqlClient(t, c, 1, "godb", "test-password", "godb")
	for _, query := range []string{
		"CREATE TABLE t(id BIGINT PRIMARY KEY,name TEXT,active BOOL)",
		"INSERT INTO t(id,name,active) VALUES(3,'c',TRUE)",
		"INSERT INTO t(id,name,active) VALUES(1,NULL,FALSE)",
		"INSERT INTO t(id,name,active) VALUES(2,'b',TRUE)",
	} {
		_, err := db.Exec(query)
		require.NoError(t, err)
	}

	before, err := c.members[1].db.AppliedPosition()
	require.NoError(t, err)
	rows, err := db.Query("SELECT id,name FROM t ORDER BY id")
	require.NoError(t, err)
	columns, values := readDiscoveryRows(t, rows)
	require.Equal(t, []string{"id", "name"}, columns)
	require.Equal(t, [][]any{{"1", nil}, {"2", "b"}, {"3", "c"}}, values)
	statement, err := db.Prepare("SELECT id FROM t WHERE active=? ORDER BY name DESC LIMIT ? OFFSET ?")
	require.NoError(t, err)
	defer func() { require.NoError(t, statement.Close()) }()
	rows, err = statement.Query(true, int64(1), int64(1))
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Equal(t, [][]any{{"2"}}, values)
	rows, err = statement.Query(nil, int64(1), int64(0))
	require.NoError(t, err)
	columns, values = readDiscoveryRows(t, rows)
	require.Equal(t, []string{"id"}, columns)
	require.Empty(t, values)
	all, err := db.Prepare("SELECT id FROM t ORDER BY id DESC LIMIT ?")
	require.NoError(t, err)
	defer func() { require.NoError(t, all.Close()) }()
	rows, err = all.Query(int64(1))
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Equal(t, [][]any{{"3"}}, values)
	after, err := c.members[1].db.AppliedPosition()
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, float64(4), testutil.ToFloat64(c.members[1].metrics.sqlSelects.WithLabelValues("table_scan", "ok")))
	require.Equal(t, float64(5), testutil.ToFloat64(c.members[1].metrics.sqlRows))
	require.NoError(t, db.Ping())
}

func TestMySQLTableScanLimit(t *testing.T) {
	c := setupCluster(t, 1)
	enableMySQL(t, c, 1)
	c.start(t, 1)
	c.leader(t)
	db := mysqlClient(t, c, 1, "godb", "test-password", "godb")
	_, err := db.Exec("CREATE TABLE t(id BIGINT PRIMARY KEY,tenant BIGINT,score BIGINT,INDEX by_score(tenant,score))")
	require.NoError(t, err)
	insert, err := db.Prepare("INSERT INTO t(id,tenant,score) VALUES(?,7,?)")
	require.NoError(t, err)
	defer func() { require.NoError(t, insert.Close()) }()

	for id := range sql.MaxSelectRows + 1 {
		_, err := insert.Exec(id, id)
		require.NoError(t, err)
	}

	_, err = db.Query("SELECT id FROM t LIMIT 0")

	var wire *driver.MySQLError

	require.ErrorAs(t, err, &wire)
	require.Equal(t, uint16(1105), wire.Number)
	statement, err := db.Prepare("SELECT id FROM t LIMIT ?")
	require.NoError(t, err)
	defer func() { require.NoError(t, statement.Close()) }()
	_, err = statement.Query(int64(1))
	require.ErrorAs(t, err, &wire)
	require.Equal(t, uint16(1105), wire.Number)
	_, err = db.Query("SELECT COUNT(*) FROM t LIMIT 0")
	require.ErrorAs(t, err, &wire)
	require.Equal(t, uint16(1105), wire.Number)
	count, err := db.Prepare("SELECT COUNT(*) FROM t LIMIT ?")
	require.NoError(t, err)
	_, err = count.Query(int64(1))
	require.ErrorAs(t, err, &wire)
	require.Equal(t, uint16(1105), wire.Number)
	require.NoError(t, count.Close())
	require.Equal(t, float64(2), testutil.ToFloat64(c.members[1].metrics.sqlAggregates.WithLabelValues("count", "error")))
	require.Zero(t, testutil.ToFloat64(c.members[1].metrics.sqlRows))
	require.Equal(t, float64(4), testutil.ToFloat64(c.members[1].metrics.sqlSelects.WithLabelValues("table_scan", "error")))
	rangeQuery, err := db.Prepare("SELECT id FROM t WHERE id>? AND id<=? ORDER BY id DESC LIMIT ?")
	require.NoError(t, err)
	defer func() { require.NoError(t, rangeQuery.Close()) }()
	rows, err := rangeQuery.Query(int64(1022), int64(1024), int64(2))
	require.NoError(t, err)
	_, values := readDiscoveryRows(t, rows)
	require.Equal(t, [][]any{{"1024"}, {"1023"}}, values)

	var total int64

	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM t WHERE id>=1024").Scan(&total))
	require.Equal(t, int64(1), total)
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM t WHERE id>1024").Scan(&total))
	require.Zero(t, total)
	rows, err = rangeQuery.Query(nil, int64(1024), int64(2))
	require.NoError(t, err)
	columns, values := readDiscoveryRows(t, rows)
	require.Equal(t, []string{"id"}, columns)
	require.Empty(t, values)
	_, err = db.Query("SELECT id FROM t WHERE id>=0 LIMIT 1")
	require.ErrorAs(t, err, &wire)
	require.Equal(t, uint16(1105), wire.Number)
	require.Equal(t, float64(4), testutil.ToFloat64(c.members[1].metrics.sqlSelects.WithLabelValues("primary_range", "ok")))
	require.Equal(t, float64(1), testutil.ToFloat64(c.members[1].metrics.sqlSelects.WithLabelValues("primary_range", "error")))
	secondary, err := db.Prepare("SELECT id FROM t WHERE tenant=? AND score>? AND score<=? ORDER BY id DESC LIMIT ?")
	require.NoError(t, err)
	defer func() { require.NoError(t, secondary.Close()) }()
	rows, err = secondary.Query(int64(7), int64(1022), int64(1024), int64(2))
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Equal(t, [][]any{{"1024"}, {"1023"}}, values)
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM t WHERE tenant=7 AND score>=1024").Scan(&total))
	require.Equal(t, int64(1), total)
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM t WHERE tenant=7 AND score>1024").Scan(&total))
	require.Zero(t, total)
	rows, err = secondary.Query(int64(7), nil, int64(1024), int64(2))
	require.NoError(t, err)
	columns, values = readDiscoveryRows(t, rows)
	require.Equal(t, []string{"id"}, columns)
	require.Empty(t, values)
	_, err = db.Query("SELECT id FROM t WHERE tenant=7 AND score>=0 LIMIT 1")
	require.ErrorAs(t, err, &wire)
	require.Equal(t, uint16(1105), wire.Number)
	require.Equal(t, float64(4), testutil.ToFloat64(c.members[1].metrics.sqlSelects.WithLabelValues("index_range", "ok")))
	require.Equal(t, float64(1), testutil.ToFloat64(c.members[1].metrics.sqlSelects.WithLabelValues("index_range", "error")))
	rows, err = db.Query("EXPLAIN SELECT id FROM t LIMIT 0")
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Len(t, values, 1)
	require.Equal(t, "table_scan", values[0][1])
	require.Equal(t, "1024", values[0][7])
	require.Equal(t, "0", values[0][8])
	require.NoError(t, db.Ping())
}
