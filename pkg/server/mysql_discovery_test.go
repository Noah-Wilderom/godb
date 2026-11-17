package server

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"uuid"

	driver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func readDiscoveryRows(t testing.TB, rows *sql.Rows) ([]string, [][]any) {
	t.Helper()
	defer func() { require.NoError(t, rows.Close()) }()
	columns, err := rows.Columns()
	require.NoError(t, err)

	var result [][]any

	for rows.Next() {
		values := make([]sql.NullString, len(columns))

		dest := make([]any, len(columns))
		for i := range dest {
			dest[i] = &values[i]
		}

		require.NoError(t, rows.Scan(dest...))
		row := make([]any, len(columns))

		for i, value := range values {
			if value.Valid {
				row[i] = value.String
			}
		}

		result = append(result, row)
	}

	require.NoError(t, rows.Err())
	return columns, result
}

func TestMySQLSchemaDiscoveryTextAndNative(t *testing.T) {
	c := setupCluster(t, 1)
	enableMySQL(t, c, 1)
	c.start(t, 1)
	c.leader(t)
	db := mysqlClient(t, c, 1, "godb", "test-password", "godb")
	rows, err := db.Query("SHOW TABLES")
	require.NoError(t, err)
	columns, values := readDiscoveryRows(t, rows)
	require.Equal(t, []string{"Tables_in_godb"}, columns)
	require.Empty(t, values)
	stmt, err := db.Prepare("SHOW FULL TABLES LIKE ?")
	require.NoError(t, err)
	defer func() { require.NoError(t, stmt.Close()) }()

	for i := range 270 {
		_, err := db.Exec(fmt.Sprintf("CREATE TABLE item_%03d (id BIGINT UNSIGNED PRIMARY KEY, name TEXT, enabled BOOLEAN NOT NULL)", i))
		require.NoError(t, err)
	}

	position, err := c.members[1].db.AppliedPosition()
	require.NoError(t, err)
	rows, err = stmt.Query("item_%")
	require.NoError(t, err)
	columns, values = readDiscoveryRows(t, rows)
	require.Equal(t, []string{"Tables_in_godb", "Table_type"}, columns)
	require.Len(t, values, 270)
	require.Equal(t, []any{"item_000", "BASE TABLE"}, values[0])
	require.Equal(t, []any{"item_269", "BASE TABLE"}, values[269])

	for _, query := range []string{"DESCRIBE item_000", "SHOW COLUMNS FROM godb.item_000", "SHOW FIELDS FROM item_000 IN godb"} {
		rows, err := db.Query(query)
		require.NoError(t, err)
		columns, values := readDiscoveryRows(t, rows)
		require.Equal(t, []string{"Field", "Type", "Null", "Key", "Default", "Extra"}, columns)
		require.Equal(t, [][]any{{"id", "bigint unsigned", "NO", "PRI", nil, ""}, {"name", "text", "YES", "", nil, ""}, {"enabled", "tinyint(1)", "NO", "", nil, ""}}, values)
	}

	columnsStmt, err := db.Prepare("SHOW FULL COLUMNS FROM item_000 LIKE ?")
	require.NoError(t, err)
	rows, err = columnsStmt.Query("name")
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Equal(t, [][]any{{"name", "text", "utf8mb4_bin", "YES", "", nil, "", "select,insert", ""}}, values)
	require.NoError(t, columnsStmt.Close())
	after, err := c.members[1].db.AppliedPosition()
	require.NoError(t, err)
	require.Equal(t, position, after)

	for _, entry := range []struct {
		query string
		code  uint16
	}{{"SHOW COLUMNS FROM missing", 1146}, {"SHOW TABLES FROM other", 1049}, {"SHOW TABLES WHERE 1=1", 1064}} {
		_, err := db.Query(entry.query)

		var wire *driver.MySQLError

		require.ErrorAs(t, err, &wire)
		require.Equal(t, entry.code, wire.Number)
	}

	require.NoError(t, db.Ping())
	families, err := c.members[1].metrics.registry.Gather()
	require.NoError(t, err)
	found := 0

	for _, family := range families {
		switch family.GetName() {
		case "godb_mysql_catalog_duration_seconds":
			require.Positive(t, family.Metric[0].Histogram.GetSampleCount())
			found++
		case "godb_mysql_discovery_commands_total", "godb_mysql_discovery_errors_total":
			require.Positive(t, family.Metric[0].Counter.GetValue())
			found++
		}
	}

	require.Equal(t, 3, found)
}

func TestMySQLDiscoverySecondaryIndexes(t *testing.T) {
	c := setupCluster(t, 1)
	enableMySQL(t, c, 1)
	c.start(t, 1)
	c.leader(t)
	table := schema.Table{ID: 1, Name: "indexed", PrimaryKeyColumnID: 1, Columns: []schema.Column{{ID: 1, Name: "id", Type: record.TypeInt64}, {ID: 2, Name: "name", Type: record.TypeText, Nullable: true}, {ID: 3, Name: "group", Type: record.TypeInt64}}, Indexes: []schema.Index{{ID: 1, Name: "name_unique", Unique: true, ColumnIDs: []uint32{2}}, {ID: 2, Name: "group_name", ColumnIDs: []uint32{3, 2}}}}
	command, err := database.EncodeCommand(database.Command{Operations: []database.CommandOperation{{Kind: database.CommandCreateTable, Table: &table}}})
	require.NoError(t, err)
	result, err := c.members[1].Propose(context.Background(), uuid.NewV4(), command)
	require.NoError(t, err)
	require.Equal(t, database.CommandOK, result.Command.Code)
	db := mysqlClient(t, c, 1, "godb", "test-password", "godb")
	stmt, err := db.Prepare("SHOW KEYS FROM indexed")
	require.NoError(t, err)
	defer func() { require.NoError(t, stmt.Close()) }()
	rows, err := stmt.Query()
	require.NoError(t, err)
	columns, values := readDiscoveryRows(t, rows)
	require.Len(t, columns, 15)
	require.Len(t, values, 4)
	require.Equal(t, []any{"indexed", "0", "PRIMARY", "1", "id", "A", nil, nil, nil, "", "BTREE", "", "", "YES", nil}, values[0])
	require.Equal(t, []any{"indexed", "0", "name_unique", "1", "name", "A", nil, nil, nil, "YES", "BTREE", "", "", "YES", nil}, values[1])
	require.Equal(t, []any{"indexed", "1", "group_name", "2", "name", "A", nil, nil, nil, "YES", "BTREE", "", "", "YES", nil}, values[3])
	rows, err = db.Query("SELECT INDEX_NAME,SEQ_IN_INDEX,COLUMN_NAME,NON_UNIQUE FROM information_schema.statistics WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? ORDER BY INDEX_NAME,SEQ_IN_INDEX", "indexed")
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Equal(t, [][]any{{"PRIMARY", "1", "id", "0"}, {"group_name", "1", "group", "1"}, {"group_name", "2", "name", "1"}, {"name_unique", "1", "name", "0"}}, values)
}

func TestMySQLDiscoveryRequiresQuorum(t *testing.T) {
	c := setupCluster(t, 3)
	for id := uint64(1); id <= 3; id++ {
		enableMySQL(t, c, id)
		c.start(t, id)
	}

	leader := c.leader(t)
	db := mysqlClient(t, c, leader, "godb", "test-password", "godb")
	_, err := db.Exec("CREATE TABLE visible (id BIGINT PRIMARY KEY)")
	require.NoError(t, err)
	stmt, err := db.Prepare("SHOW TABLES")
	require.NoError(t, err)
	defer func() { require.NoError(t, stmt.Close()) }()
	follower := mysqlClient(t, c, leader%3+1, "godb", "test-password", "godb")
	_, err = follower.Query("SHOW TABLES")

	var wire *driver.MySQLError

	require.ErrorAs(t, err, &wire)
	require.Equal(t, uint16(1290), wire.Number)
	_, err = follower.Prepare("SHOW COLUMNS FROM visible")
	require.ErrorAs(t, err, &wire)
	require.Equal(t, uint16(1290), wire.Number)

	for id, member := range c.members {
		if id != leader {
			require.NoError(t, member.Close())
		}
	}

	_, err = stmt.Query()
	require.ErrorAs(t, err, &wire)
	require.True(t, wire.Number == 1290 || wire.Number == 3024)
}
