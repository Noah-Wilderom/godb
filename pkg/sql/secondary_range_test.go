package sql

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
)

func TestSQLSecondaryRangesOnLargeTable(t *testing.T) {
	db, path := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,tenant BIGINT,score BIGINT,mirror BIGINT,active BOOL,INDEX by_score(tenant,score))")

	for id := range int64(MaxSelectRows + 20) {
		execute(t, engine, "INSERT INTO t(id,tenant,score,mirror,active) VALUES(?,7,?,?,TRUE)", record.NewInt64(id), record.NewInt64(id), record.NewInt64(id))
	}

	execute(t, engine, "INSERT INTO t(id,tenant,score,active) VALUES(2000,8,1024,TRUE)")
	check := func(engine *LocalEngine) {
		t.Helper()

		for _, test := range []struct {
			where string
			count int64
		}{
			{"tenant=7 AND score>1023", 20},
			{"tenant=7 AND score>=1024 AND score<1034", 10},
			{"tenant=7 AND score>1024 AND score>=1025 AND score<=1028 AND score<1028", 3},
			{"score<1028 AND score<=1028 AND score>=1025 AND score>1024 AND tenant=7", 3},
			{"tenant=7 AND score>=1024 AND score>1024 AND score<=1024", 0},
			{"tenant=7 AND score>1000 AND score<1", 0},
			{"tenant=7 AND score<0", 0},
			{"tenant=7 AND score>9223372036854775807", 0},
			{"tenant=7 AND score>=1024 AND active=FALSE", 0},
			{"tenant=7 AND score>=20", MaxSelectRows},
			{"tenant=9 AND score>=0", 0},
		} {
			result := execute(t, engine, "SELECT COUNT(*) FROM t WHERE "+test.where)
			require.Equal(t, [][]record.Value{{record.NewInt64(test.count)}}, result.Rows, test.where)
		}

		result := execute(t, engine, "SELECT id FROM t WHERE tenant=? AND score>=? AND score<? ORDER BY id DESC LIMIT ? OFFSET ?", record.NewInt64(7), record.NewInt64(1024), record.NewInt64(1028), record.NewInt64(2), record.NewInt64(1))
		require.Equal(t, [][]record.Value{{record.NewInt64(1026)}, {record.NewInt64(1025)}}, result.Rows)

		for _, query := range []string{
			"SELECT id FROM t WHERE tenant=7 AND score>=19 LIMIT 1",
			"SELECT COUNT(*) FROM t WHERE tenant=7 AND score>=19 LIMIT 0",
			"SELECT id FROM t WHERE tenant=7 AND score>=19 AND active=FALSE",
			"SELECT id FROM t WHERE tenant=7 AND mirror>1023",
		} {
			result, err := engine.Execute(context.Background(), query)
			require.ErrorIs(t, err, ErrLimit, query)
			require.Equal(t, Result{}, result)
		}
	}
	before := db.Stats().Writes
	check(engine)
	require.Equal(t, before, db.Stats().Writes)
	require.Equal(t, float64(3), testutil.ToFloat64(engine.metrics.selects.WithLabelValues("index_range", "error")))
	require.Equal(t, float64(12), testutil.ToFloat64(engine.metrics.selects.WithLabelValues("index_range", "ok")))
	description, err := Describe(db, "SELECT id FROM t WHERE tenant=? AND score>=? AND score<? LIMIT ?")
	require.NoError(t, err)
	require.Len(t, description.Parameters, 4)
	require.Equal(t, record.TypeInt64, description.Parameters[1].Type)
	require.NoError(t, db.Close())
	require.NoError(t, database.Validate(path))
	reopened, err := database.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	again, err := NewLocal(reopened)
	require.NoError(t, err)
	check(again)
}

func TestSQLSecondaryRangeSelectionAndNulls(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,a BIGINT,b BIGINT,c BIGINT,token BIGINT UNIQUE,INDEX abc(a,b,c),INDEX ab(a,b),INDEX other_ab(a,b),INDEX c_only(c))")
	execute(t, engine, "INSERT INTO t(id,a,b,c,token) VALUES(1,7,10,2,100)")
	execute(t, engine, "INSERT INTO t(id,a,b,c,token) VALUES(2,7,10,3,200)")
	execute(t, engine, "INSERT INTO t(id,a,b,c,token) VALUES(3,NULL,10,2,300)")
	execute(t, engine, "INSERT INTO t(id,a,b,c,token) VALUES(4,NULL,NULL,2,NULL)")

	for _, test := range []struct {
		where, path string
		ids         []int64
	}{
		{"a=7 AND b>=10", "range:3", []int64{1, 2}},
		{"a=7 AND b>10", "range:3", nil},
		{"a=7 AND b<=10", "range:3", []int64{1, 2}},
		{"a IS NULL AND b<=10", "range:3", []int64{3}},
		{"a IS NULL AND b>10", "range:3", nil},
		{"a IS NULL AND b>=NULL", "", nil},
		{"a=7 AND b=10 AND c>2", "range:2", []int64{2}},
		{"a=7 AND c>2", "scan:3", []int64{2}},
		{"a=7 AND b>=10 AND c=3", "range:3", []int64{2}},
		{"a=7 AND b>=10 AND id>0", "primary_range", []int64{1, 2}},
		{"a=7 AND b>=10 AND token=100", "unique:1", []int64{1}},
		{"a=7 AND b>=10 AND id=1", "primary", []int64{1}},
		{"token>=100 AND token<=200", "range:1", []int64{1, 2}},
		{"token<200", "range:1", []int64{1}},
		{"b>9", "table_scan", []int64{1, 2, 3}},
		{"c=2 AND token>0", "scan:5", []int64{1, 3}},
	} {
		plan, err := Prepare(db, "SELECT id FROM t WHERE "+test.where+" ORDER BY id", PrepareOptions{})
		require.NoError(t, err)
		reader := &selectReader{Database: db}
		result, err := plan.Read(reader)
		require.NoError(t, err)
		require.Equal(t, test.path, reader.path, test.where)
		require.Len(t, result.Rows, len(test.ids), test.where)

		for i, id := range test.ids {
			require.Equal(t, record.NewInt64(id), result.Rows[i][0], test.where)
		}
	}
}

func BenchmarkSQLSecondaryRange(b *testing.B) {
	db, _ := openSQLDatabase(b)
	engine, err := NewLocal(db)
	require.NoError(b, err)
	execute(b, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,tenant BIGINT,score BIGINT,mirror BIGINT,INDEX by_score(tenant,score))")

	for id := range int64(1024) {
		execute(b, engine, "INSERT INTO t(id,tenant,score,mirror) VALUES(?,7,?,?)", record.NewInt64(id), record.NewInt64(id), record.NewInt64(id))
	}

	for _, test := range []struct{ name, query string }{
		{"seek_24", "SELECT COUNT(*) FROM t WHERE tenant=7 AND score>=1000"},
		{"filter_1024", "SELECT COUNT(*) FROM t WHERE tenant=7 AND mirror>=1000"},
		{"empty_seek", "SELECT COUNT(*) FROM t WHERE tenant=7 AND score>2000"},
	} {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				if _, err := engine.Execute(context.Background(), test.query); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
