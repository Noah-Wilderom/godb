package server

import (
	"math"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestMySQLComparisons(t *testing.T) {
	c := setupCluster(t, 1)
	enableMySQL(t, c, 1)
	c.start(t, 1)
	c.leader(t)

	db := mysqlClient(t, c, 1, "godb", "test-password", "godb")
	for _, query := range []string{
		"CREATE TABLE t(id BIGINT PRIMARY KEY,score BIGINT UNSIGNED,label TEXT,bucket BIGINT,INDEX by_bucket(bucket))",
		"INSERT INTO t(id,score,label,bucket) VALUES(1,9007199254740992,'a',1)",
		"INSERT INTO t(id,score,label,bucket) VALUES(2,9007199254740993,'b',1)",
		"INSERT INTO t(id,score,label,bucket) VALUES(3,18446744073709551615,'c',1)",
		"INSERT INTO t(id,bucket) VALUES(4,1)",
	} {
		_, err := db.Exec(query)
		require.NoError(t, err)
	}

	before, err := c.members[1].db.AppliedPosition()
	require.NoError(t, err)
	rows, err := db.Query("SELECT id FROM t WHERE score>9007199254740992 ORDER BY id")
	require.NoError(t, err)
	_, values := readDiscoveryRows(t, rows)
	require.Equal(t, [][]any{{"2"}, {"3"}}, values)
	statement, err := db.Prepare("SELECT id FROM t WHERE bucket=? AND score>? AND score<=? ORDER BY score LIMIT ? OFFSET ?")
	require.NoError(t, err)
	defer func() { require.NoError(t, statement.Close()) }()
	rows, err = statement.Query(int64(1), uint64(9007199254740992), uint64(math.MaxUint64), int64(1), int64(1))
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Equal(t, [][]any{{"3"}}, values)
	rows, err = statement.Query(int64(1), nil, uint64(math.MaxUint64), int64(1), int64(0))
	require.NoError(t, err)
	columns, values := readDiscoveryRows(t, rows)
	require.Equal(t, []string{"id"}, columns)
	require.Empty(t, values)

	var count int64

	require.NoError(t, db.QueryRow("SELECT COUNT(*) AS aggregate FROM t WHERE score>?", uint64(math.MaxUint64-1)).Scan(&count))
	require.Equal(t, int64(1), count)
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM t WHERE score<=9007199254740993").Scan(&count))
	require.Equal(t, int64(2), count)
	text, err := db.Prepare("SELECT id FROM t WHERE label>=? AND label<? AND score IS NOT NULL ORDER BY id")
	require.NoError(t, err)
	rows, err = text.Query("b", "d")
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Equal(t, [][]any{{"2"}, {"3"}}, values)
	require.NoError(t, text.Close())

	for _, query := range []string{"SELECT id FROM t WHERE score>-1", "SELECT id FROM t WHERE score>'1'", "SELECT id FROM t WHERE score< =1", "DELETE FROM t WHERE id>1"} {
		_, err := db.Query(query)
		require.Error(t, err, query)
	}

	after, err := c.members[1].db.AppliedPosition()
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, float64(4), testutil.ToFloat64(c.members[1].metrics.sqlSelects.WithLabelValues("table_scan", "ok")))
	require.Equal(t, float64(2), testutil.ToFloat64(c.members[1].metrics.sqlSelects.WithLabelValues("index_scan", "ok")))
	require.Equal(t, float64(7), testutil.ToFloat64(c.members[1].metrics.sqlRows))
	require.NoError(t, db.Ping())
}
