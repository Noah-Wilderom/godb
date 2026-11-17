package sql

import (
	"context"
	"fmt"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func TestSQLSelectSecondaryIndexes(t *testing.T) {
	db, path := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE accounts (id BIGINT PRIMARY KEY,handle TEXT,tenant BIGINT UNSIGNED,token TEXT UNIQUE,note TEXT,KEY handle_lookup(handle),UNIQUE KEY tenant_handle(tenant,handle))")

	for _, query := range []string{
		"INSERT INTO accounts(id,handle,tenant,token) VALUES (3,'alice',7,'c')",
		"INSERT INTO accounts(id,handle,tenant,token) VALUES (1,'alice',8,'a')",
		"INSERT INTO accounts(id,handle,tenant,token) VALUES (2,'bob',7,'b')",
		"INSERT INTO accounts(id,handle,tenant) VALUES (4,NULL,7)",
		"INSERT INTO accounts(id,handle,tenant) VALUES (5,'alice-extra',9)",
	} {
		execute(t, engine, query)
	}

	check := func(engine *LocalEngine) {
		t.Helper()
		result := execute(t, engine, "SELECT id,handle,note,id FROM accounts WHERE handle=?", textValue(t, "alice"))
		require.Equal(t, [][]record.Value{
			{record.NewInt64(1), textValue(t, "alice"), record.NewNull(), record.NewInt64(1)},
			{record.NewInt64(3), textValue(t, "alice"), record.NewNull(), record.NewInt64(3)},
		}, result.Rows)
		result = execute(t, engine, "SELECT id FROM accounts WHERE token='b'")
		require.Equal(t, [][]record.Value{{record.NewInt64(2)}}, result.Rows)
		result = execute(t, engine, "SELECT id FROM accounts WHERE tenant=7")
		require.ElementsMatch(t, [][]record.Value{{record.NewInt64(2)}, {record.NewInt64(3)}, {record.NewInt64(4)}}, result.Rows)

		for _, query := range []string{
			"SELECT * FROM accounts WHERE handle='missing'", "SELECT * FROM accounts WHERE token='missing'",
			"SELECT * FROM accounts WHERE tenant=99", "SELECT * FROM accounts WHERE handle=NULL",
			"SELECT * FROM accounts WHERE token=NULL", "SELECT * FROM accounts WHERE tenant=NULL",
		} {
			require.Empty(t, execute(t, engine, query).Rows)
		}

		require.Empty(t, execute(t, engine, "SELECT * FROM accounts WHERE note='a'").Rows)
		_, err := engine.Execute(context.Background(), "SELECT * FROM accounts WHERE tenant=-1")
		require.ErrorIs(t, err, ErrType)
	}
	before := db.Stats().Writes
	check(engine)
	require.Equal(t, before, db.Stats().Writes)
	require.Equal(t, float64(6), testutil.ToFloat64(engine.metrics.rows))
	description, err := Describe(db, "SELECT id,note FROM accounts WHERE tenant=?")
	require.NoError(t, err)
	require.Len(t, description.Parameters, 1)
	require.Equal(t, record.TypeUint64, description.Parameters[0].Type)
	require.True(t, description.Parameters[0].Nullable)
	require.Equal(t, "id", description.Columns[0].Name)
	require.Equal(t, "note", description.Columns[1].Name)
	require.NoError(t, db.Close())
	reopened, err := database.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	engine, err = NewLocal(reopened)
	require.NoError(t, err)
	check(engine)
}

func TestSQLSelectCompositeFallsBackWithoutLeadingColumn(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE pairs(id BIGINT PRIMARY KEY,a BIGINT,b BIGINT,KEY pair(a,b))")
	require.Empty(t, execute(t, engine, "SELECT * FROM pairs WHERE b=1").Rows)
	require.Empty(t, execute(t, engine, "SELECT * FROM pairs WHERE a=1").Rows)
}

func TestSQLSelectSecondaryLimit(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE many(id BIGINT PRIMARY KEY,bucket BIGINT,KEY bucket_idx(bucket))")

	for id := range int64(MaxSelectRows) {
		execute(t, engine, "INSERT INTO many(id,bucket) VALUES (?,1)", record.NewInt64(id))
	}

	result := execute(t, engine, "SELECT id FROM many WHERE bucket=1")
	require.Len(t, result.Rows, MaxSelectRows)
	require.Equal(t, []record.Value{record.NewInt64(0)}, result.Rows[0])
	require.Equal(t, []record.Value{record.NewInt64(MaxSelectRows - 1)}, result.Rows[MaxSelectRows-1])
	execute(t, engine, "INSERT INTO many(id,bucket) VALUES (?,1)", record.NewInt64(MaxSelectRows))
	result, err = engine.Execute(context.Background(), "SELECT id FROM many WHERE bucket=1")
	require.ErrorIs(t, err, ErrLimit)
	require.Empty(t, result.Rows)
	require.Equal(t, float64(MaxSelectRows), testutil.ToFloat64(engine.metrics.rows))
	require.Len(t, execute(t, engine, "SELECT id FROM many WHERE id=0").Rows, 1)
}

type selectReader struct {
	*database.Database

	path string
}

func (r *selectReader) ScanRowsByIndexRangePage(table schema.Table, id uint32, lower, upper *database.KeyBound, limit int, cursor *database.ScanCursor, keys ...record.Value) (database.RowPage, error) {
	r.path = fmt.Sprintf("range:%d", id)
	return r.Database.ScanRowsByIndexRangePage(table, id, lower, upper, limit, cursor, keys...)
}

func (r *selectReader) ScanRowsRangePage(table schema.Table, lower, upper *database.KeyBound, limit int, cursor *database.ScanCursor) (database.RowPage, error) {
	r.path = "primary_range"
	return r.Database.ScanRowsRangePage(table, lower, upper, limit, cursor)
}

func (r *selectReader) ScanRowsPage(table schema.Table, limit int, cursor *database.ScanCursor) (database.RowPage, error) {
	r.path = "table_scan"
	return r.Database.ScanRowsPage(table, limit, cursor)
}

func (r *selectReader) GetRow(table schema.Table, key record.Value) (schema.Row, bool, error) {
	r.path = "primary"
	return r.Database.GetRow(table, key)
}

func (r *selectReader) GetRowByUniqueIndex(table schema.Table, id uint32, keys ...record.Value) (schema.Row, bool, error) {
	r.path = fmt.Sprintf("unique:%d", id)
	return r.Database.GetRowByUniqueIndex(table, id, keys...)
}

func (r *selectReader) ScanRowsByIndexPage(table schema.Table, id uint32, limit int, cursor *database.ScanCursor, keys ...record.Value) (database.RowPage, error) {
	r.path = fmt.Sprintf("scan:%d", id)
	return r.Database.ScanRowsByIndexPage(table, id, limit, cursor, keys...)
}

func TestSQLSelectAccessPaths(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE paths(id BIGINT PRIMARY KEY,a BIGINT,b BIGINT,c BIGINT,KEY longer(a,b),KEY first(a),KEY second(a),KEY ordinary(b),UNIQUE(b),UNIQUE(c,a),KEY pk(id))")
	execute(t, engine, "INSERT INTO paths(id,a,b,c) VALUES(1,2,3,4)")

	for _, test := range []struct{ query, path string }{
		{"SELECT * FROM paths WHERE id=1", "primary"},
		{"SELECT * FROM paths", "table_scan"},
		{"SELECT * FROM paths WHERE a=2", "scan:2"},
		{"SELECT * FROM paths WHERE b=3", "unique:5"},
		{"SELECT * FROM paths WHERE c=4", "scan:6"},
		{"SELECT * FROM paths WHERE b=NULL", ""},
	} {
		t.Run(test.query, func(t *testing.T) {
			plan, err := Prepare(db, test.query, PrepareOptions{})
			require.NoError(t, err)
			reader := &selectReader{Database: db}
			result, err := plan.Read(reader)
			require.NoError(t, err)
			require.Equal(t, test.path, reader.path)

			if test.path != "" {
				require.Len(t, result.Rows, 1)
			}
		})
	}
}
