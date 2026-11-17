package sql

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func TestCreateIndexDefinitionsAreDeterministic(t *testing.T) {
	query := `CREATE TABLE accounts (
 UNIQUE KEY pair (tenant,handle),
 id BIGINT PRIMARY KEY,
 handle TEXT,
 tenant BIGINT NOT NULL,
 token TEXT UNIQUE KEY,
 INDEX lookup (handle,tenant),
 CONSTRAINT token_named UNIQUE (token)
 )`
	plan, err := Prepare(nil, query, PrepareOptions{CreateTableID: 7})
	require.NoError(t, err)
	require.Equal(t, []schema.Index{
		{ID: 1, Name: "pair", ColumnIDs: []uint32{3, 2}, Unique: true},
		{ID: 2, Name: "token", ColumnIDs: []uint32{4}, Unique: true},
		{ID: 3, Name: "lookup", ColumnIDs: []uint32{2, 3}},
		{ID: 4, Name: "token_named", ColumnIDs: []uint32{4}, Unique: true},
	}, plan.table.Indexes)
	command, err := plan.Command()
	require.NoError(t, err)

	for range 10 {
		again, err := Prepare(nil, query, PrepareOptions{CreateTableID: 7})
		require.NoError(t, err)
		encoded, err := again.Command()
		require.NoError(t, err)
		require.Equal(t, command, encoded)
	}

	for _, declaration := range []string{"UNIQUE (token)", "UNIQUE KEY (token)", "UNIQUE INDEX (token)", "UNIQUE named (token)", "UNIQUE KEY named (token)", "CONSTRAINT named UNIQUE KEY (token)", "CONSTRAINT named UNIQUE INDEX (token)"} {
		plan, err := Prepare(nil, "CREATE TABLE t (id BIGINT PRIMARY KEY,token TEXT,"+declaration+")", PrepareOptions{CreateTableID: 1})
		require.NoError(t, err, declaration)
		require.Len(t, plan.table.Indexes, 1)
		require.True(t, plan.table.Indexes[0].Unique)
	}
}

func TestCreateGeneratedIndexNames(t *testing.T) {
	plan, err := Prepare(nil, "CREATE TABLE t (id BIGINT PRIMARY KEY,x TEXT UNIQUE,KEY x(x),INDEX(x),UNIQUE(x))", PrepareOptions{CreateTableID: 1})
	require.NoError(t, err)

	var names []string

	for _, index := range plan.table.Indexes {
		names = append(names, index.Name)
	}

	require.Equal(t, []string{"x_2", "x", "x_3", "x_4"}, names)
	name := strings.Repeat("日", 21)
	query := "CREATE TABLE t (id BIGINT PRIMARY KEY,`" + name + "` TEXT UNIQUE,KEY(`" + name + "`))"
	plan, err = Prepare(nil, query, PrepareOptions{CreateTableID: 1})
	require.NoError(t, err)
	require.Equal(t, name, plan.table.Indexes[0].Name)
	require.Equal(t, strings.Repeat("日", 20)+"_2", plan.table.Indexes[1].Name)

	for _, index := range plan.table.Indexes {
		require.True(t, utf8.ValidString(index.Name))
		require.LessOrEqual(t, len(index.Name), MaxIdentifierBytes)
	}

	plan, err = Prepare(nil, "CREATE TABLE t (id BIGINT PRIMARY KEY,`PRIMARY` TEXT UNIQUE)", PrepareOptions{CreateTableID: 1})
	require.NoError(t, err)
	require.Equal(t, "PRIMARY_2", plan.table.Indexes[0].Name)
}

func TestCreateRejectsInvalidIndexesWithoutWrites(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	before := db.Stats().Writes

	for _, entry := range []struct {
		definition string
		err        error
	}{
		{"KEY duplicate(id),UNIQUE DUPLICATE(id)", ErrIndexName},
		{"INDEX `PRIMARY`(id)", ErrIndexName},
		{"KEY lookup(missing)", ErrColumn},
		{"KEY lookup(id,id)", ErrColumn},
		{"flag BOOLEAN,KEY lookup(flag)", ErrType},
	} {
		query := "CREATE TABLE invalid (id BIGINT PRIMARY KEY," + entry.definition + ")"
		_, err := engine.Execute(context.Background(), query)
		require.ErrorIs(t, err, entry.err)
		_, err = Describe(db, query)
		require.ErrorIs(t, err, entry.err)
	}

	for _, definition := range []string{
		"KEY lookup()", "KEY lookup(id DESC)", "KEY lookup(id ASC)", "KEY lookup(id(4))", "KEY lookup((id))", "KEY lookup(?)",
		"KEY lookup USING HASH(id)", "KEY lookup(id) INVISIBLE", "FOREIGN KEY(id) REFERENCES other(id)", "CHECK(id=1)",
		"token TEXT UNIQUE UNIQUE", "token TEXT UNIQUE INDEX", "CONSTRAINT named UNIQUE other(id)", "CONSTRAINT named PRIMARY KEY(id)",
	} {
		_, err := engine.Execute(context.Background(), "CREATE TABLE invalid (id BIGINT PRIMARY KEY,"+definition+")")
		require.Error(t, err, definition)
	}

	require.Equal(t, before, db.Stats().Writes)
	_, found, err := db.GetTableByName("invalid")
	require.NoError(t, err)
	require.False(t, found)

	var parts []string

	parts = append(parts, "id BIGINT PRIMARY KEY")
	for i := range MaxIndexes {
		parts = append(parts, fmt.Sprintf("KEY i%d(id)", i))
	}

	statement, err := Parse("CREATE TABLE t (" + strings.Join(parts, ",") + ")")
	require.NoError(t, err)
	require.Len(t, statement.indexes, MaxIndexes)
	parts = append(parts, "KEY too_many(id)")
	_, err = Parse("CREATE TABLE t (" + strings.Join(parts, ",") + ")")
	require.ErrorIs(t, err, ErrLimit)
}

func TestLocalSQLIndexesEnforceAndPersist(t *testing.T) {
	db, path := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	query := "CREATE TABLE accounts (id BIGINT PRIMARY KEY,handle TEXT,tenant BIGINT NOT NULL,token TEXT UNIQUE,UNIQUE KEY tenant_handle(tenant,handle),KEY handle_lookup(handle))"
	before := db.Stats().Writes
	_, err = Describe(db, query)
	require.NoError(t, err)
	require.Equal(t, before, db.Stats().Writes)
	execute(t, engine, query)
	execute(t, engine, "INSERT INTO accounts (id,handle,tenant,token) VALUES (1,'alice',7,'a')")

	for _, query := range []string{
		"INSERT INTO accounts (id,handle,tenant,token) VALUES (2,'bob',8,'a')",
		"INSERT INTO accounts (id,handle,tenant,token) VALUES (2,'alice',7,'b')",
	} {
		_, err := engine.Execute(context.Background(), query)

		var rejected *CommandError

		require.ErrorAs(t, err, &rejected)
		require.Equal(t, database.CommandUniqueConflict, rejected.Code)
	}

	execute(t, engine, "INSERT INTO accounts (id,handle,tenant,token) VALUES (2,'alice',8,'b')")

	for _, id := range []int64{3, 4} {
		execute(t, engine, "INSERT INTO accounts (id,tenant) VALUES (?,9)", record.NewInt64(id))
	}

	table, found, err := db.GetTableByName("accounts")
	require.NoError(t, err)
	require.True(t, found)
	rows, err := db.ScanRowsByIndex(table, 3, 10, textValue(t, "alice"))
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.NoError(t, db.Close())
	reopened, err := database.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	stored, found, err := reopened.GetTableByName("accounts")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, table, stored)
	rows, err = reopened.ScanRowsByIndex(stored, 3, 10, textValue(t, "alice"))
	require.NoError(t, err)
	require.Len(t, rows, 2)
	engine, err = NewLocal(reopened)
	require.NoError(t, err)
	_, err = engine.Execute(context.Background(), "INSERT INTO accounts (id,handle,tenant) VALUES (5,'alice',7)")
	require.Error(t, err)
}

func BenchmarkPrepareCreateIndexes(b *testing.B) {
	query := "CREATE TABLE accounts (id BIGINT PRIMARY KEY,handle TEXT UNIQUE,tenant BIGINT,UNIQUE KEY tenant_handle(tenant,handle),INDEX lookup(handle))"
	for b.Loop() {
		_, err := Prepare(nil, query, PrepareOptions{CreateTableID: 1})
		if err != nil {
			b.Fatal(err)
		}
	}
}
