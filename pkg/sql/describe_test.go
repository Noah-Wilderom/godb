package sql

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func TestDescribeValidatesWithoutWritingOrBindingValues(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE users (id BIGINT PRIMARY KEY, name TEXT NOT NULL, enabled BOOLEAN)")
	before := db.Stats().Writes
	description, err := Describe(db, "INSERT INTO users (name,enabled,id) VALUES (?,?,?)")
	require.NoError(t, err)
	require.Empty(t, description.Columns)
	require.Len(t, description.Parameters, 3)
	require.Equal(t, record.TypeText, description.Parameters[0].Type)
	require.Equal(t, record.TypeBool, description.Parameters[1].Type)
	require.Equal(t, record.TypeInt64, description.Parameters[2].Type)
	description, err = Describe(db, "SELECT name,id FROM users WHERE id=?")
	require.NoError(t, err)
	require.Len(t, description.Parameters, 1)
	require.True(t, description.Parameters[0].Nullable)
	require.Equal(t, "name", description.Columns[0].Name)
	require.Equal(t, "id", description.Columns[1].Name)
	require.False(t, description.Columns[1].Nullable)
	description.Columns[0].Name = "changed"
	again, err := Describe(db, "SELECT name,id FROM users WHERE id=?")
	require.NoError(t, err)
	require.Equal(t, "name", again.Columns[0].Name)

	for _, query := range []string{
		"INSERT INTO users (id) VALUES (?)",
		"INSERT INTO users (id,name,name) VALUES (?,?,'duplicate')",
		"INSERT INTO users (id,name) VALUES ('wrong',?)",
		"SELECT missing FROM users WHERE id=?",
		"INSERT INTO missing (id) VALUES (?)",
	} {
		_, err := Describe(db, query)
		require.Error(t, err, query)
	}

	description, err = Describe(db, "CREATE TABLE later (id BIGINT PRIMARY KEY)")
	require.NoError(t, err)
	require.Empty(t, description.Columns)
	require.Empty(t, description.Parameters)
	_, found, err := db.GetTableByName("later")
	require.NoError(t, err)
	require.False(t, found)
	require.Equal(t, before, db.Stats().Writes)
}
