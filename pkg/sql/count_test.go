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

func TestSQLCountSemanticsAndReopen(t *testing.T) {
	db, path := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,name TEXT UNIQUE,bucket BIGINT,active BOOL,INDEX by_bucket(bucket))")
	require.Equal(t, [][]record.Value{{record.NewInt64(0)}}, execute(t, engine, "SELECT COUNT(*) FROM t").Rows)

	for _, query := range []string{"INSERT INTO t(id,name,bucket,active) VALUES(1,'',7,FALSE)", "INSERT INTO t(id,name,bucket,active) VALUES(2,'a',7,TRUE)", "INSERT INTO t(id,bucket) VALUES(3,8)"} {
		execute(t, engine, query)
	}

	check := func(engine *LocalEngine) {
		t.Helper()

		for _, test := range []struct {
			query, name string
			count       int64
		}{
			{"SELECT COUNT(*) FROM t", "COUNT(*)", 3},
			{"SELECT count(name) AS total FROM t", "total", 2},
			{"SELECT COUNT(active) FROM t", "COUNT(active)", 2},
			{"SELECT COUNT(id) FROM t", "COUNT(id)", 3},
			{"SELECT COUNT(*) AS aggregate FROM t WHERE bucket=7", "aggregate", 2},
			{"SELECT COUNT(name) FROM t WHERE bucket=8", "COUNT(name)", 0},
			{"SELECT COUNT(*) FROM t WHERE id=2", "COUNT(*)", 1},
			{"SELECT COUNT(*) FROM t WHERE name='a'", "COUNT(*)", 1},
			{"SELECT COUNT(*) FROM t WHERE id=99", "COUNT(*)", 0},
			{"SELECT COUNT(name) FROM t WHERE active=TRUE", "COUNT(name)", 1},
			{"SELECT COUNT(*) FROM t WHERE active=NULL", "COUNT(*)", 0},
			{"SELECT COUNT(*) FROM t WHERE bucket=7 AND active=FALSE", "COUNT(*)", 1},
			{"SELECT COUNT(*) FROM t WHERE id=1 AND id=2", "COUNT(*)", 0},
			{"SELECT COUNT(*) FROM t LIMIT 1", "COUNT(*)", 3},
			{"SELECT COUNT(*) FROM t LIMIT 0,1", "COUNT(*)", 3},
		} {
			result := execute(t, engine, test.query)
			require.Equal(t, []schema.Column{{Name: test.name, Type: record.TypeInt64}}, result.Columns, test.query)
			require.Equal(t, [][]record.Value{{record.NewInt64(test.count)}}, result.Rows, test.query)
			require.Zero(t, result.AffectedRows)
		}

		for _, query := range []string{"SELECT COUNT(*) FROM t LIMIT 0", "SELECT COUNT(*) FROM t LIMIT 1 OFFSET 1", "SELECT COUNT(name) FROM t WHERE active=NULL LIMIT 0", "SELECT COUNT(*) FROM t LIMIT 1,1"} {
			result := execute(t, engine, query)
			require.Empty(t, result.Rows)
			require.Len(t, result.Columns, 1)
		}
	}
	before := db.Stats().Writes
	check(engine)
	require.Equal(t, before, db.Stats().Writes)
	require.Equal(t, float64(20), testutil.ToFloat64(engine.metrics.aggregates.WithLabelValues("count", "ok")))
	require.Equal(t, float64(16), testutil.ToFloat64(engine.metrics.rows))
	execute(t, engine, "UPDATE t SET name=NULL WHERE id=2")
	require.Equal(t, [][]record.Value{{record.NewInt64(1)}}, execute(t, engine, "SELECT COUNT(name) FROM t").Rows)
	execute(t, engine, "UPDATE t SET name='a' WHERE id=2")
	require.NoError(t, db.Close())
	require.NoError(t, database.Validate(path))
	reopened, err := database.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	again, err := NewLocal(reopened)
	require.NoError(t, err)
	check(again)
}

func TestSQLCountBindingDescriptionAndValidation(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,name TEXT,active BOOL,count BIGINT)")
	execute(t, engine, "INSERT INTO t(id,name,active,count) VALUES(1,'a',TRUE,42)")
	before := db.Stats().Writes
	description, err := Describe(db, "SELECT COUNT(name) AS `total rows` FROM t WHERE active=? LIMIT ? OFFSET ?")
	require.NoError(t, err)
	require.Equal(t, []schema.Column{{Name: "total rows", Type: record.TypeInt64}}, description.Columns)
	require.Len(t, description.Parameters, 3)
	require.Equal(t, record.TypeBool, description.Parameters[0].Type)
	require.True(t, description.Parameters[0].Nullable)
	require.Equal(t, record.TypeUint64, description.Parameters[1].Type)
	require.False(t, description.Parameters[1].Nullable)
	result := execute(t, engine, "SELECT COUNT(name) AS aggregate FROM t WHERE active=? LIMIT ? OFFSET ?", record.NewBool(true), record.NewInt64(1), record.NewInt64(0))
	require.Equal(t, [][]record.Value{{record.NewInt64(1)}}, result.Rows)
	require.Equal(t, [][]record.Value{{record.NewInt64(42)}}, execute(t, engine, "SELECT count FROM t").Rows)
	require.Equal(t, [][]record.Value{{record.NewInt64(1)}}, execute(t, engine, "SELECT COUNT(count) FROM t").Rows)

	for _, test := range []struct {
		query  string
		args   []record.Value
		target error
	}{
		{"SELECT COUNT(missing) FROM t WHERE id=NULL", nil, ErrColumn},
		{"SELECT COUNT(*) FROM t WHERE active='bad'", nil, ErrType},
		{"SELECT COUNT(*) FROM missing", nil, database.ErrTableNotFound},
		{"SELECT COUNT(*) FROM t LIMIT ?", nil, ErrBinding},
		{"SELECT COUNT(*) FROM t LIMIT ?", []record.Value{record.NewNull()}, ErrType},
		{"SELECT COUNT(*) FROM t", []record.Value{record.NewInt64(1)}, ErrBinding},
		{"SELECT COUNT(*) FROM t ORDER BY id", nil, ErrUnsupported},
		{"SELECT COUNT(*) FROM t LIMIT 1025", nil, ErrLimit},
	} {
		_, err := engine.Execute(context.Background(), test.query, test.args...)
		require.ErrorIs(t, err, test.target, test.query)
	}

	for _, query := range []string{"SELECT COUNT() FROM t", "SELECT COUNT(1) FROM t", "SELECT COUNT(NULL) FROM t", "SELECT COUNT(?) FROM t", "SELECT COUNT(DISTINCT name) FROM t", "SELECT COUNT(*),id FROM t", "SELECT id,COUNT(*) FROM t", "SELECT COUNT(*),COUNT(name) FROM t", "SELECT COUNT(*) FROM t GROUP BY id", "SELECT COUNT(*) FROM t HAVING id=1", "SELECT COUNT(*) total FROM t", "SELECT COUNT(*) AS FROM t", "SELECT COUNT(*) AS ? FROM t", "SELECT COUNT(*) OVER() FROM t", "SELECT COUNT(*) AS `" + strings.Repeat("x", 65) + "` FROM t"} {
		_, err := Parse(query)
		require.ErrorIs(t, err, ErrSyntax, query)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = engine.Execute(ctx, "SELECT COUNT(*) FROM t")
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, before, db.Stats().Writes)
}

func TestSQLCountCandidateBound(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,bucket BIGINT,active BOOL,INDEX by_bucket(bucket))")

	for id := range int64(MaxSelectRows) {
		execute(t, engine, "INSERT INTO t(id,bucket,active) VALUES(?,1,TRUE)", record.NewInt64(id))
	}

	require.Equal(t, [][]record.Value{{record.NewInt64(MaxSelectRows)}}, execute(t, engine, "SELECT COUNT(*) FROM t").Rows)
	execute(t, engine, "INSERT INTO t(id,bucket,active) VALUES(?,1,TRUE)", record.NewInt64(MaxSelectRows))

	for _, query := range []string{"SELECT COUNT(*) FROM t", "SELECT COUNT(active) FROM t WHERE bucket=1", "SELECT COUNT(*) FROM t WHERE active=FALSE", "SELECT COUNT(*) FROM t LIMIT 0", "SELECT COUNT(*) FROM t LIMIT 1 OFFSET 1"} {
		result, err := engine.Execute(context.Background(), query)
		require.ErrorIs(t, err, ErrLimit, query)
		require.Equal(t, Result{}, result)
	}

	require.Equal(t, float64(5), testutil.ToFloat64(engine.metrics.aggregates.WithLabelValues("count", "error")))
	require.Equal(t, float64(1), testutil.ToFloat64(engine.metrics.rows))
	require.Equal(t, [][]record.Value{{record.NewInt64(1)}}, execute(t, engine, "SELECT COUNT(*) FROM t WHERE id=0").Rows)
	require.Equal(t, [][]record.Value{{record.NewInt64(0)}}, execute(t, engine, "SELECT COUNT(*) FROM t WHERE id=NULL").Rows)
}
