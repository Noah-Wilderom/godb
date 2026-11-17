package server

import (
	"testing"

	driver "github.com/go-sql-driver/mysql"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/sql"
)

func TestMySQLSelectSecondaryIndexes(t *testing.T) {
	c := setupCluster(t, 1)
	enableMySQL(t, c, 1)
	c.start(t, 1)
	c.leader(t)
	db := mysqlClient(t, c, 1, "godb", "test-password", "godb")
	_, err := db.Exec("CREATE TABLE accounts(id BIGINT PRIMARY KEY,handle TEXT,tenant BIGINT UNSIGNED,token TEXT UNIQUE,note TEXT,KEY handle_idx(handle),UNIQUE KEY tenant_handle(tenant,handle))")
	require.NoError(t, err)
	insert, err := db.Prepare("INSERT INTO accounts(id,handle,tenant,token) VALUES (?,?,?,?)")
	require.NoError(t, err)
	defer func() { require.NoError(t, insert.Close()) }()

	handle := "O'Reilly\x00;-- café"
	for _, row := range [][]any{{int64(3), handle, uint64(7), "c"}, {int64(1), handle, uint64(8), "a"}, {int64(2), "bob", uint64(7), "b"}, {int64(4), nil, uint64(7), nil}} {
		_, err := insert.Exec(row...)
		require.NoError(t, err)
	}

	before, err := c.members[1].db.AppliedPosition()
	require.NoError(t, err)
	statement, err := db.Prepare("SELECT id,handle,note FROM accounts WHERE handle=?")
	require.NoError(t, err)
	defer func() { require.NoError(t, statement.Close()) }()
	rows, err := statement.Query(handle)
	require.NoError(t, err)
	columns, values := readDiscoveryRows(t, rows)
	require.Equal(t, []string{"id", "handle", "note"}, columns)
	require.Equal(t, [][]any{{"1", handle, nil}, {"3", handle, nil}}, values)

	for _, value := range []any{nil, "missing"} {
		rows, err := statement.Query(value)
		require.NoError(t, err)
		columns, values := readDiscoveryRows(t, rows)
		require.Len(t, columns, 3)
		require.Empty(t, values)
	}

	rows, err = db.Query("SELECT id FROM accounts WHERE tenant=7")
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.ElementsMatch(t, [][]any{{"2"}, {"3"}, {"4"}}, values)

	var id int64

	require.NoError(t, db.QueryRow("SELECT id FROM accounts WHERE token=?", "b").Scan(&id))
	require.Equal(t, int64(2), id)
	composite, err := db.Prepare("SELECT id FROM accounts WHERE handle=? AND tenant=? AND note=?")
	require.NoError(t, err)
	rows, err = composite.Query(handle, uint64(7), nil)
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Empty(t, values)
	require.NoError(t, composite.Close())
	composite, err = db.Prepare("SELECT id FROM accounts WHERE handle=? AND tenant=?")
	require.NoError(t, err)

	for _, test := range []struct {
		tenant uint64
		id     int64
	}{{7, 3}, {8, 1}} {
		require.NoError(t, composite.QueryRow(handle, test.tenant).Scan(&id))
		require.Equal(t, test.id, id)
	}

	rows, err = composite.Query(handle, uint64(9))
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Empty(t, values)
	require.NoError(t, composite.Close())
	rows, err = db.Query("SELECT id FROM accounts WHERE tenant=7 AND token='b'")
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Equal(t, [][]any{{"2"}}, values)
	rows, err = db.Query("SELECT id FROM accounts WHERE id=1 AND tenant=7")
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Empty(t, values)
	rows, err = db.Query("SELECT id FROM accounts WHERE note='a'")
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Empty(t, values)
	after, err := c.members[1].db.AppliedPosition()
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, float64(9), testutil.ToFloat64(c.members[1].metrics.sqlRows))
	require.NoError(t, db.Ping())
}

func TestMySQLSelectSecondaryLimit(t *testing.T) {
	c := setupCluster(t, 1)
	enableMySQL(t, c, 1)
	c.start(t, 1)
	c.leader(t)
	db := mysqlClient(t, c, 1, "godb", "test-password", "godb")
	_, err := db.Exec("CREATE TABLE many(id BIGINT PRIMARY KEY,bucket BIGINT,KEY bucket_idx(bucket))")
	require.NoError(t, err)
	insert, err := db.Prepare("INSERT INTO many(id,bucket) VALUES (?,1)")
	require.NoError(t, err)
	defer func() { require.NoError(t, insert.Close()) }()

	for id := range sql.MaxSelectRows {
		_, err := insert.Exec(id)
		require.NoError(t, err)
	}

	statement, err := db.Prepare("SELECT id FROM many WHERE bucket=?")
	require.NoError(t, err)
	defer func() { require.NoError(t, statement.Close()) }()
	rows, err := statement.Query(int64(1))
	require.NoError(t, err)
	_, values := readDiscoveryRows(t, rows)
	require.Len(t, values, sql.MaxSelectRows)
	_, err = insert.Exec(sql.MaxSelectRows)
	require.NoError(t, err)
	rows, err = statement.Query(int64(1))

	var wire *driver.MySQLError

	require.ErrorAs(t, err, &wire)
	require.Equal(t, uint16(1105), wire.Number)
	require.Nil(t, rows)
	rows, err = db.Query("SELECT id FROM many WHERE bucket=1")
	require.ErrorAs(t, err, &wire)
	require.Equal(t, uint16(1105), wire.Number)
	require.Nil(t, rows)
	require.Equal(t, float64(sql.MaxSelectRows), testutil.ToFloat64(c.members[1].metrics.sqlRows))
	require.NoError(t, db.Ping())
}
