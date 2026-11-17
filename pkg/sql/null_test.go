package sql

import (
	"context"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func TestSQLNullPredicatesAndReopen(t *testing.T) {
	db, path := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,tenant BIGINT NOT NULL,token TEXT UNIQUE,nickname TEXT,active BOOL,note TEXT,score BIGINT,INDEX nick(nickname),UNIQUE pair(tenant,nickname))")

	for _, query := range []string{
		"INSERT INTO t(id,tenant) VALUES(1,7)",
		"INSERT INTO t(id,tenant,active,note,score) VALUES(2,7,FALSE,'',0)",
		"INSERT INTO t(id,tenant,token,nickname,active,note) VALUES(3,7,'x','alice',TRUE,'present')",
		"INSERT INTO t(id,tenant,token,active) VALUES(4,8,'y',TRUE)",
		"INSERT INTO t(id,tenant,token,nickname,active,note) VALUES(5,8,'','',FALSE,'zero')",
	} {
		execute(t, engine, query)
	}

	check := func(engine *LocalEngine) {
		t.Helper()

		for _, test := range []struct {
			where string
			ids   []int64
		}{
			{"token IS NULL", []int64{1, 2}},
			{"token IS NOT NULL", []int64{3, 4, 5}},
			{"nickname IS NULL", []int64{1, 2, 4}},
			{"nickname IS NOT NULL", []int64{3, 5}},
			{"active IS NULL", []int64{1}},
			{"active IS NOT NULL", []int64{2, 3, 4, 5}},
			{"score IS NOT NULL", []int64{2}},
			{"note IS NOT NULL", []int64{2, 3, 5}},
			{"tenant=7 AND nickname IS NULL", []int64{1, 2}},
			{"nickname IS NULL AND tenant=7", []int64{1, 2}},
			{"tenant=7 AND nickname IS NOT NULL", []int64{3}},
			{"id IS NULL", nil},
			{"id IS NOT NULL", []int64{1, 2, 3, 4, 5}},
			{"tenant IS NULL", nil},
			{"nickname IS NULL AND nickname IS NOT NULL", nil},
			{"token IS NULL AND token='x'", nil},
			{"token='x' AND token IS NULL", nil},
			{"token=NULL", nil},
			{"nickname IS NULL AND token=NULL", nil},
			{"token IS NOT NULL AND active=FALSE", []int64{5}},
			{"token IS NULL AND token IS NULL", []int64{1, 2}},
		} {
			result := execute(t, engine, "SELECT id FROM t WHERE "+test.where+" ORDER BY id")

			want := make([][]record.Value, 0, len(test.ids))
			for _, id := range test.ids {
				want = append(want, []record.Value{record.NewInt64(id)})
			}

			require.Equal(t, want, result.Rows, test.where)
			require.Len(t, result.Columns, 1)
		}

		for _, test := range []struct {
			query string
			count int64
		}{
			{"SELECT COUNT(*) FROM t WHERE token IS NULL", 2},
			{"SELECT COUNT(token) FROM t WHERE token IS NULL", 0},
			{"SELECT COUNT(token) FROM t WHERE token IS NOT NULL", 3},
			{"SELECT COUNT(*) FROM t WHERE tenant IS NULL", 0},
			{"SELECT COUNT(*) FROM t WHERE active IS NULL AND active IS NOT NULL", 0},
		} {
			require.Equal(t, [][]record.Value{{record.NewInt64(test.count)}}, execute(t, engine, test.query).Rows)
		}

		result := execute(t, engine, "SELECT id FROM t WHERE nickname IS NULL AND tenant=? ORDER BY id DESC LIMIT ? OFFSET ?", record.NewInt64(7), record.NewInt64(1), record.NewInt64(1))
		require.Equal(t, [][]record.Value{{record.NewInt64(1)}}, result.Rows)
	}
	before := db.Stats().Writes
	check(engine)
	require.Equal(t, before, db.Stats().Writes)
	require.NoError(t, db.Close())
	require.NoError(t, database.Validate(path))
	reopened, err := database.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	again, err := NewLocal(reopened)
	require.NoError(t, err)
	check(again)
}

func TestSQLNullPredicateAccessPaths(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,a TEXT UNIQUE,b BIGINT,active BOOL,UNIQUE pair(a,b),INDEX b_idx(b))")
	execute(t, engine, "INSERT INTO t(id,b) VALUES(1,7)")
	execute(t, engine, "INSERT INTO t(id,b) VALUES(2,7)")

	for _, test := range []struct {
		where, path string
		count       int
	}{
		{"a IS NULL", "scan:1", 2},
		{"a IS NULL AND b=7", "scan:2", 2},
		{"b=7 AND a IS NULL", "scan:2", 2},
		{"a IS NULL AND b IS NULL", "scan:2", 0},
		{"a IS NOT NULL", "table_scan", 0},
		{"a IS NOT NULL AND b=7", "scan:3", 0},
		{"a IS NULL AND id=1", "primary", 1},
		{"a IS NULL AND id IS NULL", "", 0},
		{"active IS NULL", "table_scan", 2},
		{"a='missing' AND b IS NULL", "unique:1", 0},
	} {
		plan, err := Prepare(db, "SELECT id FROM t WHERE "+test.where, PrepareOptions{})
		require.NoError(t, err)
		reader := &selectReader{Database: db}
		result, err := plan.Read(reader)
		require.NoError(t, err)
		require.Equal(t, test.path, reader.path, test.where)
		require.Len(t, result.Rows, test.count, test.where)
	}

	execute(t, engine, "SELECT id FROM t WHERE a IS NULL")
	require.Equal(t, float64(1), testutil.ToFloat64(engine.metrics.selects.WithLabelValues("index_scan", "ok")))
	require.Equal(t, float64(2), testutil.ToFloat64(engine.metrics.rows))
}

func TestSQLNullPredicateDescriptionAndValidation(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,note TEXT,active BOOL)")
	before := db.Stats().Writes
	description, err := Describe(db, "SELECT COUNT(*) AS aggregate FROM t WHERE note IS NULL AND active=? AND id IS NOT NULL LIMIT ? OFFSET ?")
	require.NoError(t, err)
	require.Len(t, description.Parameters, 3)
	require.Equal(t, record.TypeBool, description.Parameters[0].Type)
	require.Equal(t, record.TypeUint64, description.Parameters[1].Type)
	require.Equal(t, record.TypeUint64, description.Parameters[2].Type)
	description, err = Describe(db, "SELECT note FROM t WHERE note IS NOT NULL")
	require.NoError(t, err)
	require.Empty(t, description.Parameters)

	for _, test := range []struct {
		query  string
		target error
	}{
		{"SELECT id FROM t WHERE missing IS NULL", ErrColumn},
		{"SELECT id FROM t WHERE id IS NULL AND missing IS NOT NULL", ErrColumn},
		{"SELECT id FROM t WHERE id IS NULL AND active='bad'", ErrType},
		{"SELECT id FROM t WHERE note IS NULL LIMIT 1025", ErrLimit},
	} {
		_, err := engine.Execute(context.Background(), test.query)
		require.ErrorIs(t, err, test.target, test.query)
	}

	_, err = engine.Execute(context.Background(), "SELECT id FROM t WHERE note IS NULL", record.NewNull())
	require.ErrorIs(t, err, ErrBinding)

	for _, query := range []string{
		"SELECT id FROM t WHERE note IS", "SELECT id FROM t WHERE note IS NOT", "SELECT id FROM t WHERE note IS ?",
		"SELECT id FROM t WHERE note IS NOT ?", "SELECT id FROM t WHERE note IS TRUE", "SELECT id FROM t WHERE note IS NOT FALSE",
		"SELECT id FROM t WHERE note NOT NULL", "SELECT id FROM t WHERE note IS NULL NULL", "SELECT id FROM t WHERE note IS NULL OR id=1",
		"SELECT id FROM t WHERE note IS NULL AND", "UPDATE t SET note='x' WHERE id IS NULL", "DELETE FROM t WHERE id IS NOT NULL",
	} {
		_, err := Parse(query)
		require.ErrorIs(t, err, ErrSyntax, query)
	}

	_, err = Parse("SELECT id FROM t WHERE " + strings.Repeat("note IS NULL AND ", MaxPredicates-1) + "note IS NOT NULL")
	require.NoError(t, err)
	_, err = Parse("SELECT id FROM t WHERE " + strings.Repeat("note IS NULL AND ", MaxPredicates) + "note IS NOT NULL")
	require.ErrorIs(t, err, ErrLimit)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = engine.Execute(ctx, "SELECT COUNT(*) FROM t WHERE note IS NULL")
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, before, db.Stats().Writes)
}

func TestSQLNullPredicateCandidateBound(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,token TEXT UNIQUE,active BOOL)")

	for id := range int64(MaxSelectRows) {
		execute(t, engine, "INSERT INTO t(id) VALUES(?)", record.NewInt64(id))
	}

	require.Len(t, execute(t, engine, "SELECT id FROM t WHERE token IS NULL").Rows, MaxSelectRows)
	execute(t, engine, "INSERT INTO t(id) VALUES(?)", record.NewInt64(MaxSelectRows))

	for _, query := range []string{
		"SELECT id FROM t WHERE token IS NULL", "SELECT COUNT(*) FROM t WHERE token IS NULL LIMIT 0",
		"SELECT id FROM t WHERE token IS NULL AND active IS NOT NULL", "SELECT id FROM t WHERE token IS NOT NULL LIMIT 1",
	} {
		result, err := engine.Execute(context.Background(), query)
		require.ErrorIs(t, err, ErrLimit, query)
		require.Equal(t, Result{}, result)
	}

	require.Equal(t, float64(MaxSelectRows), testutil.ToFloat64(engine.metrics.rows))
	require.Len(t, execute(t, engine, "SELECT id FROM t WHERE id=0 AND token IS NULL").Rows, 1)
	require.Equal(t, [][]record.Value{{record.NewInt64(0)}}, execute(t, engine, "SELECT COUNT(*) FROM t WHERE id IS NULL").Rows)
}

func TestSQLNullPredicatesOnNativeTypes(t *testing.T) {
	db, _ := openSQLDatabase(t)
	table := schema.Table{ID: 1, Name: "native", PrimaryKeyColumnID: 1, Columns: []schema.Column{
		{ID: 1, Name: "id", Type: record.TypeUint64},
		{ID: 2, Name: "document", Type: record.TypeJSON, Nullable: true},
	}}
	require.NoError(t, db.CreateTable(table))
	jsonNull, err := record.NewJSON([]byte("null"))
	require.NoError(t, err)
	require.NoError(t, db.InsertRow(table, schema.Row{1: record.NewUint64(1), 2: record.NewNull()}))
	require.NoError(t, db.InsertRow(table, schema.Row{1: record.NewUint64(2), 2: jsonNull}))
	engine, err := NewLocal(db)
	require.NoError(t, err)
	require.Equal(t, [][]record.Value{{record.NewUint64(1)}}, execute(t, engine, "SELECT id FROM native WHERE document IS NULL").Rows)
	require.Equal(t, [][]record.Value{{record.NewUint64(2)}}, execute(t, engine, "SELECT id FROM native WHERE document IS NOT NULL").Rows)
	require.Equal(t, [][]record.Value{{record.NewInt64(1)}}, execute(t, engine, "SELECT COUNT(document) FROM native WHERE document IS NOT NULL").Rows)
}
