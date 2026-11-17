package server

import (
	"context"
	"database/sql"
	"testing"

	driver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

func TestMySQLInformationSchemaClientQueries(t *testing.T) {
	c := setupCluster(t, 1)
	enableMySQL(t, c, 1)
	c.start(t, 1)
	c.leader(t)
	db := mysqlClient(t, c, 1, "godb", "test-password", "godb")
	tables, err := db.Prepare("SELECT TABLE_NAME AS name,ENGINE,TABLE_ROWS FROM information_schema.tables WHERE TABLE_SCHEMA=? AND TABLE_TYPE='BASE TABLE' ORDER BY name")
	require.NoError(t, err)
	defer func() { require.NoError(t, tables.Close()) }()
	rows, err := tables.Query("godb")
	require.NoError(t, err)
	names, values := readDiscoveryRows(t, rows)
	require.Equal(t, []string{"name", "ENGINE", "TABLE_ROWS"}, names)
	require.Empty(t, values)
	_, err = db.Exec("CREATE TABLE beta (id BIGINT UNSIGNED PRIMARY KEY, email TEXT, enabled BOOLEAN)")
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE alpha (id BIGINT PRIMARY KEY)")
	require.NoError(t, err)
	before, err := c.members[1].db.AppliedPosition()
	require.NoError(t, err)
	rows, err = tables.Query("godb")
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Equal(t, [][]any{{"alpha", "GoDB", nil}, {"beta", "GoDB", nil}}, values)
	rows, err = tables.Query("missing")
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Empty(t, values)
	columns, err := db.Prepare("SELECT c.COLUMN_NAME AS name,DATA_TYPE,COLUMN_TYPE,IS_NULLABLE,COLUMN_DEFAULT,NUMERIC_PRECISION FROM information_schema.columns c WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? ORDER BY ORDINAL_POSITION")
	require.NoError(t, err)
	defer func() { require.NoError(t, columns.Close()) }()
	rows, err = columns.Query("beta")
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Equal(t, [][]any{{"id", "bigint", "bigint unsigned", "NO", nil, "20"}, {"email", "text", "text", "YES", nil, nil}, {"enabled", "tinyint", "tinyint(1)", "YES", nil, "1"}}, values)

	var count int64

	require.NoError(t, db.QueryRow("SELECT COUNT(*) AS aggregate FROM information_schema.tables WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", "beta").Scan(&count))
	require.Equal(t, int64(1), count)
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE TABLE_NAME=?", "beta' OR 1=1 --").Scan(&count))
	require.Zero(t, count)

	var column string

	require.NoError(t, db.QueryRow("SELECT COLUMN_NAME FROM information_schema.columns WHERE TABLE_NAME=? AND ORDINAL_POSITION=?", "beta", int64(2)).Scan(&column))
	require.Equal(t, "email", column)
	err = db.QueryRow("SELECT COLUMN_NAME FROM information_schema.columns WHERE TABLE_NAME=? AND ORDINAL_POSITION=?", "beta", nil).Scan(&column)
	require.ErrorIs(t, err, sql.ErrNoRows)
	rows, err = db.Query("SELECT COLUMN_NAME FROM information_schema.columns WHERE TABLE_NAME='beta' ORDER BY ORDINAL_POSITION DESC LIMIT 1 OFFSET 1")
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Equal(t, [][]any{{"email"}}, values)

	var schema string

	require.NoError(t, db.QueryRow("SELECT SCHEMA_NAME FROM information_schema.schemata WHERE SCHEMA_NAME=DATABASE()").Scan(&schema))
	require.Equal(t, "godb", schema)

	for _, query := range []string{"SELECT missing FROM information_schema.tables", "SELECT * FROM information_schema.tables WHERE TABLE_NAME='alpha' OR TABLE_NAME='beta'", "SELECT * FROM information_schema.tables; SELECT 1", "SELECT * FROM information_schema.columns WHERE ORDINAL_POSITION>1"} {
		_, err := db.Query(query)

		var wire *driver.MySQLError

		require.ErrorAs(t, err, &wire, query)
	}

	require.NoError(t, db.Ping())
	after, err := c.members[1].db.AppliedPosition()
	require.NoError(t, err)
	require.Equal(t, before, after)
	families, err := c.members[1].metrics.registry.Gather()
	require.NoError(t, err)
	found := 0

	for _, family := range families {
		switch family.GetName() {
		case "godb_mysql_information_schema_commands_total", "godb_mysql_information_schema_errors_total":
			require.Positive(t, family.Metric[0].Counter.GetValue())
			found++
		}
	}

	require.Equal(t, 2, found)
}

func TestMySQLInformationSchemaRequiresQuorum(t *testing.T) {
	c := setupCluster(t, 3)
	for id := uint64(1); id <= 3; id++ {
		enableMySQL(t, c, id)
		c.start(t, id)
	}

	leader := c.leader(t)
	db := mysqlClient(t, c, leader, "godb", "test-password", "godb")
	stmt, err := db.Prepare("SELECT COUNT(*) FROM information_schema.tables WHERE TABLE_SCHEMA=DATABASE()")
	require.NoError(t, err)
	defer func() { require.NoError(t, stmt.Close()) }()

	follower := mysqlClient(t, c, leader%3+1, "godb", "test-password", "godb")
	for _, query := range []string{"SELECT * FROM information_schema.schemata", "SELECT COUNT(*) FROM information_schema.tables", "SELECT * FROM information_schema.table_constraints", "SELECT * FROM information_schema.key_column_usage"} {
		_, err := follower.Query(query)

		var wire *driver.MySQLError

		require.ErrorAs(t, err, &wire)
		require.Equal(t, uint16(1290), wire.Number)
		_, err = follower.Prepare(query)
		require.ErrorAs(t, err, &wire)
		require.Equal(t, uint16(1290), wire.Number)
	}

	for id, member := range c.members {
		if id != leader {
			require.NoError(t, member.Close())
		}
	}

	var count int64

	err = stmt.QueryRowContext(context.Background()).Scan(&count)

	var wire *driver.MySQLError

	require.ErrorAs(t, err, &wire)
	require.True(t, wire.Number == 1290 || wire.Number == 3024)

	for _, query := range []string{"SELECT * FROM information_schema.table_constraints", "SELECT * FROM information_schema.key_column_usage"} {
		_, err := db.Query(query)
		require.ErrorAs(t, err, &wire)
		require.True(t, wire.Number == 1290 || wire.Number == 3024)
	}
}
