package sql

import (
	"context"
	"math"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
)

func TestSQLPrimaryRangesOnLargeTable(t *testing.T) {
	db, path := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT UNSIGNED PRIMARY KEY,token BIGINT UNSIGNED UNIQUE,bucket BIGINT,active BOOL,INDEX by_bucket(bucket))")

	for id := uint64(0); id < MaxSelectRows+20; id++ {
		execute(t, engine, "INSERT INTO t(id,token,bucket,active) VALUES(?,?,1,TRUE)", record.NewUint64(id), record.NewUint64(id))
	}

	check := func(engine *LocalEngine) {
		t.Helper()

		for _, test := range []struct {
			query string
			count int64
		}{
			{"SELECT COUNT(*) FROM t WHERE id>1023", 20},
			{"SELECT COUNT(*) FROM t WHERE id>=1024 AND id<1034", 10},
			{"SELECT COUNT(*) FROM t WHERE id>1024 AND id>=1025 AND id<=1028 AND id<1028", 3},
			{"SELECT COUNT(*) FROM t WHERE id<1028 AND id<=1028 AND id>=1025 AND id>1024", 3},
			{"SELECT COUNT(*) FROM t WHERE id>=1024 AND id>1024 AND id<=1024", 0},
			{"SELECT COUNT(*) FROM t WHERE id>1024 AND id>=1024 AND id<=1024", 0},
			{"SELECT COUNT(*) FROM t WHERE id>1000 AND id<1", 0},
			{"SELECT COUNT(*) FROM t WHERE id<0", 0},
			{"SELECT COUNT(*) FROM t WHERE id>18446744073709551615", 0},
			{"SELECT COUNT(*) FROM t WHERE id>=1024 AND bucket=1", 20},
			{"SELECT COUNT(*) FROM t WHERE bucket=2 AND id>=1024", 0},
			{"SELECT COUNT(*) FROM t WHERE id>=20", MaxSelectRows},
		} {
			result := execute(t, engine, test.query)
			require.Equal(t, [][]record.Value{{record.NewInt64(test.count)}}, result.Rows, test.query)
		}

		result := execute(t, engine, "SELECT id FROM t WHERE id>=? AND id<? ORDER BY id DESC LIMIT ? OFFSET ?", record.NewUint64(1024), record.NewUint64(1028), record.NewInt64(2), record.NewInt64(1))
		require.Equal(t, [][]record.Value{{record.NewUint64(1026)}, {record.NewUint64(1025)}}, result.Rows)
		require.Equal(t, [][]record.Value{{record.NewInt64(0)}}, execute(t, engine, "SELECT COUNT(*) FROM t WHERE id>?", record.NewNull()).Rows)

		for _, query := range []string{"SELECT id FROM t WHERE id>=19 LIMIT 1", "SELECT COUNT(*) FROM t WHERE id>=19 LIMIT 0", "SELECT id FROM t WHERE id>=19 AND active=FALSE", "SELECT id FROM t WHERE id<=1024"} {
			result, err := engine.Execute(context.Background(), query)
			require.ErrorIs(t, err, ErrLimit, query)
			require.Equal(t, Result{}, result)
		}
	}
	before := db.Stats().Writes
	check(engine)
	require.Equal(t, before, db.Stats().Writes)
	require.Equal(t, float64(4), testutil.ToFloat64(engine.metrics.selects.WithLabelValues("primary_range", "error")))

	for _, test := range []struct {
		where, path string
		rows        int
	}{
		{"id>1023 AND bucket=1", "primary_range", 20},
		{"id>1023 AND token=1024", "unique:1", 1},
		{"id>1023 AND id=1024", "primary", 1},
		{"id>1023 AND token=1", "unique:1", 0},
		{"id>1023 AND id=1", "primary", 0},
		{"bucket=1", "scan:2", 0},
	} {
		plan, err := Prepare(db, "SELECT id FROM t WHERE "+test.where, PrepareOptions{})
		require.NoError(t, err)
		reader := &selectReader{Database: db}

		result, err := plan.Read(reader)
		if test.path == "scan:2" {
			require.ErrorIs(t, err, ErrLimit)
		} else {
			require.NoError(t, err)
			require.Len(t, result.Rows, test.rows)
		}

		require.Equal(t, test.path, reader.path)
	}

	description, err := Describe(db, "SELECT id FROM t WHERE id>=? AND id<? LIMIT ?")
	require.NoError(t, err)
	require.Len(t, description.Parameters, 3)
	require.Equal(t, record.TypeUint64, description.Parameters[0].Type)
	require.True(t, description.Parameters[0].Nullable)
	require.NoError(t, db.Close())
	require.NoError(t, database.Validate(path))
	reopened, err := database.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	again, err := NewLocal(reopened)
	require.NoError(t, err)
	check(again)
}

func TestSQLPrimaryRangeTextPrefixesAndSignedEndpoints(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE names(id TEXT PRIMARY KEY)")

	for _, value := range []string{"", "a", "a\x00", "a\x00b", "aa", "b"} {
		execute(t, engine, "INSERT INTO names(id) VALUES(?)", textValue(t, value))
	}

	result := execute(t, engine, "SELECT id FROM names WHERE id>? AND id<=? ORDER BY id", textValue(t, "a"), textValue(t, "aa"))
	require.Equal(t, [][]record.Value{{textValue(t, "a\x00")}, {textValue(t, "a\x00b")}, {textValue(t, "aa")}}, result.Rows)
	require.Equal(t, [][]record.Value{{textValue(t, "")}}, execute(t, engine, "SELECT id FROM names WHERE id<=?", textValue(t, "")).Rows)
	execute(t, engine, "CREATE TABLE signed(id BIGINT PRIMARY KEY)")

	for _, id := range []int64{math.MinInt64, 0, math.MaxInt64} {
		execute(t, engine, "INSERT INTO signed(id) VALUES(?)", record.NewInt64(id))
	}

	require.Empty(t, execute(t, engine, "SELECT id FROM signed WHERE id>9223372036854775807").Rows)
	require.Empty(t, execute(t, engine, "SELECT id FROM signed WHERE id<-9223372036854775808").Rows)
	require.Equal(t, [][]record.Value{{record.NewInt64(math.MaxInt64)}}, execute(t, engine, "SELECT id FROM signed WHERE id>=9223372036854775807").Rows)
	require.Equal(t, [][]record.Value{{record.NewInt64(math.MinInt64)}}, execute(t, engine, "SELECT id FROM signed WHERE id<=-9223372036854775808").Rows)
}
