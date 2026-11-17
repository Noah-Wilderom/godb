package server

import (
	"testing"

	driver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

func TestMySQLConstraintMetadataAndEnforcement(t *testing.T) {
	c := setupCluster(t, 1)
	enableMySQL(t, c, 1)
	c.start(t, 1)
	c.leader(t)
	db := mysqlClient(t, c, 1, "godb", "test-password", "godb")
	constraints, err := db.Prepare("SELECT CONSTRAINT_NAME,CONSTRAINT_TYPE,ENFORCED FROM information_schema.table_constraints WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? ORDER BY CONSTRAINT_NAME")
	require.NoError(t, err)
	defer func() { require.NoError(t, constraints.Close()) }()
	keys, err := db.Prepare("SELECT CONSTRAINT_NAME,COLUMN_NAME,ORDINAL_POSITION,POSITION_IN_UNIQUE_CONSTRAINT,REFERENCED_TABLE_SCHEMA,REFERENCED_TABLE_NAME,REFERENCED_COLUMN_NAME FROM information_schema.key_column_usage WHERE TABLE_SCHEMA=? AND TABLE_NAME=? ORDER BY CONSTRAINT_NAME,ORDINAL_POSITION")
	require.NoError(t, err)
	defer func() { require.NoError(t, keys.Close()) }()
	rows, err := constraints.Query("accounts")
	require.NoError(t, err)
	_, values := readDiscoveryRows(t, rows)
	require.Empty(t, values)
	create, err := db.Prepare("CREATE TABLE accounts (id BIGINT PRIMARY KEY,handle TEXT,tenant BIGINT NOT NULL,token TEXT,UNIQUE KEY token_unique(token),CONSTRAINT tenant_handle UNIQUE(tenant,handle),INDEX handle_lookup(handle))")
	require.NoError(t, err)
	_, found, err := c.members[1].db.GetTableByName("accounts")
	require.NoError(t, err)
	require.False(t, found)
	_, err = create.Exec()
	require.NoError(t, err)
	require.NoError(t, create.Close())
	before, err := c.members[1].db.AppliedPosition()
	require.NoError(t, err)
	rows, err = constraints.Query("accounts")
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Equal(t, [][]any{{"PRIMARY", "PRIMARY KEY", "YES"}, {"tenant_handle", "UNIQUE", "YES"}, {"token_unique", "UNIQUE", "YES"}}, values)
	rows, err = keys.Query("godb", "accounts")
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Equal(t, [][]any{
		{"PRIMARY", "id", "1", nil, nil, nil, nil},
		{"tenant_handle", "tenant", "1", nil, nil, nil, nil},
		{"tenant_handle", "handle", "2", nil, nil, nil, nil},
		{"token_unique", "token", "1", nil, nil, nil, nil},
	}, values)
	rows, err = db.Query("SELECT COLUMN_NAME FROM information_schema.key_column_usage WHERE CONSTRAINT_NAME='tenant_handle' AND ORDINAL_POSITION=2")
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Equal(t, [][]any{{"handle"}}, values)
	rows, err = keys.Query("missing", "accounts")
	require.NoError(t, err)
	_, values = readDiscoveryRows(t, rows)
	require.Empty(t, values)

	var count int64

	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM information_schema.table_constraints WHERE CONSTRAINT_TYPE='FOREIGN KEY'").Scan(&count))
	require.Zero(t, count)
	after, err := c.members[1].db.AppliedPosition()
	require.NoError(t, err)
	require.Equal(t, before, after)
	insert, err := db.Prepare("INSERT INTO accounts (id,handle,tenant,token) VALUES (?,?,?,?)")
	require.NoError(t, err)
	defer func() { require.NoError(t, insert.Close()) }()
	_, err = insert.Exec(int64(1), "alice", int64(7), "a")
	require.NoError(t, err)

	for _, args := range [][]any{{int64(1), "bob", int64(8), "b"}, {int64(2), "bob", int64(8), "a"}, {int64(2), "alice", int64(7), "b"}} {
		_, err := insert.Exec(args...)

		var wire *driver.MySQLError

		require.ErrorAs(t, err, &wire)
		require.Equal(t, uint16(1062), wire.Number)
	}

	_, err = insert.Exec(int64(2), "alice", int64(8), "b")
	require.NoError(t, err)

	for _, id := range []int64{3, 4} {
		_, err := insert.Exec(id, nil, int64(9), nil)
		require.NoError(t, err)
	}

	_, err = db.Exec("CREATE TABLE invalid_indexes(id BIGINT PRIMARY KEY,KEY same(id),UNIQUE SAME(id))")

	var wire *driver.MySQLError

	require.ErrorAs(t, err, &wire)
	require.Equal(t, uint16(1061), wire.Number)
	_, found, err = c.members[1].db.GetTableByName("invalid_indexes")
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, db.Ping())
}
