package server

import (
	"database/sql"
	"math"
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

func TestMySQLNativeStatementsTypedValuesAndReuse(t *testing.T) {
	c := setupCluster(t, 1)
	enableMySQL(t, c, 1)
	c.start(t, 1)
	c.leader(t)
	db := mysqlClient(t, c, 1, "godb", "test-password", "godb")
	create, err := db.Prepare("CREATE TABLE native (id BIGINT UNSIGNED PRIMARY KEY, n BIGINT NOT NULL, enabled BOOLEAN, text TEXT)")
	require.NoError(t, err)
	_, found, err := c.members[1].db.GetTableByName("native")
	require.NoError(t, err)
	require.False(t, found)
	_, err = create.Exec()
	require.NoError(t, err)
	require.NoError(t, create.Close())
	table, found, err := c.members[1].db.GetTableByName("native")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint32(1), table.ID)
	insert, err := db.Prepare("INSERT INTO native (id,n,enabled,text) VALUES (?,?,?,?)")
	require.NoError(t, err)
	selectRow, err := db.Prepare("SELECT id,n,enabled,text FROM native WHERE id=?")
	require.NoError(t, err)
	hostile := "O'Reilly\\data\x00?; DROP TABLE native; -- café"

	entries := []struct {
		id            uint64
		n             int64
		enabled, text any
	}{
		{math.MaxUint64, math.MinInt64, true, hostile},
		{2, math.MaxInt64, false, ""},
		{3, -1, nil, nil},
		{4, 0, true, []byte("bytes as UTF-8")},
	}
	for _, entry := range entries {
		result, err := insert.Exec(entry.id, entry.n, entry.enabled, entry.text)
		require.NoError(t, err)
		affected, err := result.RowsAffected()
		require.NoError(t, err)
		require.Equal(t, int64(1), affected)

		var (
			id      uint64
			n       int64
			enabled sql.NullBool
			text    sql.NullString
		)

		require.NoError(t, selectRow.QueryRow(entry.id).Scan(&id, &n, &enabled, &text))
		require.Equal(t, entry.id, id)
		require.Equal(t, entry.n, n)
		require.Equal(t, entry.enabled != nil, enabled.Valid)
		require.Equal(t, entry.text != nil, text.Valid)

		if entry.enabled != nil {
			require.Equal(t, entry.enabled, enabled.Bool)
		}

		switch value := entry.text.(type) {
		case string:
			require.Equal(t, value, text.String)
		case []byte:
			require.Equal(t, string(value), text.String)
		}
	}

	var id uint64

	require.ErrorIs(t, selectRow.QueryRow(uint64(55)).Scan(&id), sql.ErrNoRows)

	for _, args := range [][]any{
		{uint64(10), uint64(math.MaxUint64), true, "overflow"},
		{int64(-1), int64(0), true, "negative unsigned"},
		{uint64(10), int64(0), true, []byte{255}},
		{uint64(10), int64(0), 2, "bad bool"},
		{uint64(10), 1.25, true, "unsupported float"},
	} {
		_, err := insert.Exec(args...)
		require.Error(t, err)
	}

	_, err = insert.Exec(uint64(10), int64(42), true, "after errors")
	require.NoError(t, err)
	_, err = insert.Exec(uint64(10), int64(42), true, "duplicate")

	var wire *driver.MySQLError

	require.ErrorAs(t, err, &wire)
	require.Equal(t, uint16(1062), wire.Number)
	require.NoError(t, insert.Close())
	require.NoError(t, selectRow.Close())
	require.NoError(t, db.Ping())
	require.Eventually(t, func() bool {
		families, err := c.members[1].metrics.registry.Gather()
		if err != nil {
			return false
		}

		for _, family := range families {
			if family.GetName() == "godb_mysql_prepared_statements" {
				return family.Metric[0].Gauge.GetValue() == 0
			}
		}

		return false
	}, time.Second, time.Millisecond)
}

func TestMySQLPreparedConnectionIsolationAndRelease(t *testing.T) {
	c := setupCluster(t, 1)
	enableMySQL(t, c, 1)
	c.start(t, 1)
	c.leader(t)
	db := mysqlClient(t, c, 1, "godb", "test-password", "godb")
	_, err := db.Exec("CREATE TABLE handles (id BIGINT PRIMARY KEY)")
	require.NoError(t, err)

	statements := make([]*sql.Stmt, 128)
	for i := range statements {
		statements[i], err = db.Prepare("SELECT * FROM handles WHERE id=?")
		require.NoError(t, err)
	}

	_, err = db.Prepare("SELECT * FROM handles WHERE id=?")

	var wire *driver.MySQLError

	require.ErrorAs(t, err, &wire)
	require.Equal(t, uint16(1461), wire.Number)
	require.NoError(t, statements[0].Close())
	replacement, err := db.Prepare("SELECT * FROM handles WHERE id=?")
	require.NoError(t, err)
	require.NoError(t, replacement.Close())
	require.NoError(t, db.Close())
	require.Eventually(t, func() bool {
		families, err := c.members[1].metrics.registry.Gather()
		if err != nil {
			return false
		}

		for _, family := range families {
			if strings.HasPrefix(family.GetName(), "godb_mysql_prepared_statement") && family.Metric[0].GetGauge().GetValue() != 0 {
				return false
			}
		}

		return true
	}, time.Second, time.Millisecond)

	for _, statement := range statements {
		require.NoError(t, statement.Close())
	}
}
