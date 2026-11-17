package sql

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
)

func TestSQLTableScanFiltersOrderingAndReopen(t *testing.T) {
	db, path := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,name TEXT,active BOOL,INDEX by_name(name))")
	execute(t, engine, "CREATE TABLE other(id BIGINT PRIMARY KEY)")
	execute(t, engine, "INSERT INTO other(id) VALUES(99)")
	empty := execute(t, engine, "SELECT * FROM t")
	require.Empty(t, empty.Rows)
	require.Len(t, empty.Columns, 3)

	for _, query := range []string{
		"INSERT INTO t(id,name,active) VALUES(3,'z',TRUE)",
		"INSERT INTO t(id,name,active) VALUES(-1,NULL,FALSE)",
		"INSERT INTO t(id,name,active) VALUES(2,'a',TRUE)",
	} {
		execute(t, engine, query)
	}

	check := func(engine *LocalEngine) {
		t.Helper()
		result := execute(t, engine, "SELECT id FROM t ORDER BY id")
		require.Equal(t, [][]record.Value{{record.NewInt64(-1)}, {record.NewInt64(2)}, {record.NewInt64(3)}}, result.Rows)
		result = execute(t, engine, "SELECT id FROM t WHERE active=? ORDER BY name DESC LIMIT ? OFFSET ?", record.NewBool(true), record.NewInt64(1), record.NewInt64(1))
		require.Equal(t, [][]record.Value{{record.NewInt64(2)}}, result.Rows)
		result = execute(t, engine, "SELECT name,id FROM t ORDER BY name LIMIT 1")
		require.Equal(t, [][]record.Value{{record.NewNull(), record.NewInt64(-1)}}, result.Rows)
		require.Empty(t, execute(t, engine, "SELECT id FROM t WHERE active=TRUE AND active=FALSE").Rows)
		require.Empty(t, execute(t, engine, "SELECT id FROM t WHERE active=NULL").Rows)
		require.Empty(t, execute(t, engine, "SELECT id FROM t LIMIT 0").Rows)
	}
	before := db.Stats().Writes
	check(engine)
	require.Equal(t, before, db.Stats().Writes)
	require.Equal(t, float64(7), testutil.ToFloat64(engine.metrics.selects.WithLabelValues("table_scan", "ok")))
	require.Equal(t, float64(5), testutil.ToFloat64(engine.metrics.rows))
	description, err := Describe(db, "SELECT id FROM t WHERE active=? ORDER BY name LIMIT ? OFFSET ?")
	require.NoError(t, err)
	require.Len(t, description.Parameters, 3)
	require.Equal(t, record.TypeBool, description.Parameters[0].Type)
	require.Equal(t, record.TypeUint64, description.Parameters[1].Type)
	require.Equal(t, record.TypeUint64, description.Parameters[2].Type)
	require.Len(t, description.Columns, 1)

	for _, query := range []string{"SELECT absent FROM t", "SELECT id FROM t ORDER BY absent", "SELECT id FROM t WHERE active='bad'", "SELECT id FROM t WHERE absent=1", "SELECT id FROM missing"} {
		_, err := engine.Execute(context.Background(), query)
		require.Error(t, err, query)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = engine.Execute(ctx, "SELECT * FROM t")
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, db.Close())
	require.NoError(t, database.Validate(path))
	reopened, err := database.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	again, err := NewLocal(reopened)
	require.NoError(t, err)
	check(again)
}

func TestSQLTableScanCandidateBoundAndIndexPreference(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,active BOOL,name TEXT UNIQUE)")

	for id := range int64(MaxSelectRows) {
		execute(t, engine, "INSERT INTO t(id,active) VALUES(?,FALSE)", record.NewInt64(id))
	}

	require.Len(t, execute(t, engine, "SELECT id FROM t").Rows, MaxSelectRows)
	execute(t, engine, "INSERT INTO t(id,active,name) VALUES(?,TRUE,'last')", record.NewInt64(MaxSelectRows))

	for _, query := range []string{"SELECT * FROM t", "SELECT id FROM t LIMIT 1", "SELECT id FROM t LIMIT 0", "SELECT id FROM t WHERE active=TRUE", "SELECT id FROM t WHERE active=TRUE ORDER BY id DESC LIMIT 1", "SELECT id FROM t WHERE active=TRUE AND active=FALSE"} {
		result, err := engine.Execute(context.Background(), query)
		require.ErrorIs(t, err, ErrLimit, query)
		require.Equal(t, Result{}, result)
	}

	require.Equal(t, float64(6), testutil.ToFloat64(engine.metrics.selects.WithLabelValues("table_scan", "error")))
	require.Equal(t, float64(MaxSelectRows), testutil.ToFloat64(engine.metrics.rows))

	for _, query := range []string{"SELECT id FROM t WHERE id=0", "SELECT id FROM t WHERE name='last'"} {
		require.Len(t, execute(t, engine, query).Rows, 1)
	}

	require.Equal(t, float64(1), testutil.ToFloat64(engine.metrics.selects.WithLabelValues("primary", "ok")))
	require.Equal(t, float64(1), testutil.ToFloat64(engine.metrics.selects.WithLabelValues("unique", "ok")))
}

func TestSQLTableScanTextPrimaryKeys(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id TEXT PRIMARY KEY)")

	for _, key := range []string{"", "a\x00b", "a", "O'Reilly;--"} {
		execute(t, engine, "INSERT INTO t(id) VALUES(?)", textValue(t, key))
	}

	result := execute(t, engine, "SELECT id FROM t ORDER BY id")
	require.Equal(t, [][]record.Value{{textValue(t, "")}, {textValue(t, "O'Reilly;--")}, {textValue(t, "a")}, {textValue(t, "a\x00b")}}, result.Rows)
}
