package server

import (
	"testing"

	driver "github.com/go-sql-driver/mysql"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestMySQLSelectWindow(t *testing.T) {
	c := setupCluster(t, 1)
	enableMySQL(t, c, 1)
	c.start(t, 1)
	c.leader(t)

	db := mysqlClient(t, c, 1, "godb", "test-password", "godb")
	for _, query := range []string{
		"CREATE TABLE ranked(id BIGINT PRIMARY KEY,bucket BIGINT,score BIGINT,label TEXT,KEY bucket_idx(bucket))",
		"INSERT INTO ranked(id,bucket,score,label) VALUES(1,1,2,'two')",
		"INSERT INTO ranked(id,bucket,score,label) VALUES(2,1,10,'ten')",
		"INSERT INTO ranked(id,bucket,score,label) VALUES(3,1,NULL,NULL)",
	} {
		_, err := db.Exec(query)
		require.NoError(t, err)
	}

	before, err := c.members[1].db.AppliedPosition()
	require.NoError(t, err)
	statement, err := db.Prepare("SELECT id,label FROM ranked WHERE bucket=? ORDER BY score DESC,id LIMIT ? OFFSET ?")
	require.NoError(t, err)
	defer func() { require.NoError(t, statement.Close()) }()

	for _, test := range []struct {
		offset int64
		want   [][]any
	}{
		{0, [][]any{{"2", "ten"}}}, {1, [][]any{{"1", "two"}}}, {2, [][]any{{"3", nil}}}, {3, nil},
	} {
		rows, err := statement.Query(int64(1), int64(1), test.offset)
		require.NoError(t, err)
		columns, values := readDiscoveryRows(t, rows)
		require.Equal(t, []string{"id", "label"}, columns)
		require.Equal(t, test.want, values)
	}

	for _, test := range []struct {
		count any
		code  uint16
	}{
		{int64(-1), 1366}, {nil, 1366}, {"1", 1366}, {int64(1025), 1105},
	} {
		_, err := statement.Query(int64(1), test.count, int64(0))

		var wire *driver.MySQLError

		require.ErrorAs(t, err, &wire)
		require.Equal(t, test.code, wire.Number)
	}

	rows, err := db.Query("SELECT id,label FROM ranked WHERE bucket=1 ORDER BY score LIMIT 1,2")
	require.NoError(t, err)
	_, values := readDiscoveryRows(t, rows)
	require.Equal(t, [][]any{{"1", "two"}, {"2", "ten"}}, values)
	comma, err := db.Prepare("SELECT id FROM ranked WHERE bucket=? ORDER BY score DESC LIMIT ?,?")
	require.NoError(t, err)

	var id int64

	require.NoError(t, comma.QueryRow(int64(1), int64(1), int64(1)).Scan(&id))
	require.Equal(t, int64(1), id)
	require.NoError(t, comma.Close())
	rows, err = statement.Query(int64(1), int64(0), int64(0))
	require.NoError(t, err)
	columns, values := readDiscoveryRows(t, rows)
	require.Len(t, columns, 2)
	require.Empty(t, values)
	after, err := c.members[1].db.AppliedPosition()
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, float64(6), testutil.ToFloat64(c.members[1].metrics.sqlRows))
	require.NoError(t, db.Ping())
}
