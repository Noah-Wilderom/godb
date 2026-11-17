package sql

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func TestSQLComparisonsTypedBoundariesAndReopen(t *testing.T) {
	db, path := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,score BIGINT,amount BIGINT UNSIGNED,label TEXT,flag BOOL)")
	signed := []int64{math.MinInt64, -1, 0, 9007199254740992, 9007199254740993, math.MaxInt64}
	amount := []uint64{0, 1, 9007199254740992, 9007199254740993, 1 << 63, math.MaxUint64}
	labels := []string{"", "A", "a", "aa", "z", "é"}

	flags := []bool{false, false, true, true, false, true}
	for i := range signed {
		execute(t, engine, "INSERT INTO t(id,score,amount,label,flag) VALUES(?,?,?,?,?)", record.NewInt64(int64(i+1)), record.NewInt64(signed[i]), record.NewUint64(amount[i]), textValue(t, labels[i]), record.NewBool(flags[i]))
	}

	execute(t, engine, "INSERT INTO t(id) VALUES(7)")
	check := func(engine *LocalEngine) {
		t.Helper()

		for _, test := range []struct {
			where string
			ids   []int64
		}{
			{"score<0", []int64{1, 2}},
			{"score<=0", []int64{1, 2, 3}},
			{"score>9007199254740992", []int64{5, 6}},
			{"score>=9007199254740992", []int64{4, 5, 6}},
			{"score<=-9223372036854775808", []int64{1}},
			{"score>=9223372036854775807", []int64{6}},
			{"amount>18446744073709551614", []int64{6}},
			{"amount<=0", []int64{1}},
			{"amount>9223372036854775807", []int64{5, 6}},
			{"amount<9223372036854775807", []int64{1, 2, 3, 4}},
			{"amount>9007199254740992 AND amount<=9007199254740993", []int64{4}},
			{"label>'a'", []int64{4, 5, 6}},
			{"label<='a'", []int64{1, 2, 3}},
			{"flag>FALSE", []int64{3, 4, 6}},
			{"flag<TRUE", []int64{1, 2, 5}},
			{"score>=0 AND score<9007199254740993 AND label IS NOT NULL", []int64{3, 4}},
			{"score>=1 AND score<0", nil},
			{"score>=NULL", nil},
			{"label<NULL", nil},
			{"score IS NULL AND id>0", []int64{7}},
			{"id>0 AND id=3", []int64{3}},
		} {
			result := execute(t, engine, "SELECT id FROM t WHERE "+test.where+" ORDER BY id")

			want := make([][]record.Value, 0, len(test.ids))
			for _, id := range test.ids {
				want = append(want, []record.Value{record.NewInt64(id)})
			}

			require.Equal(t, want, result.Rows, test.where)
		}

		result := execute(t, engine, "SELECT id FROM t WHERE score>=? AND score<? ORDER BY score DESC LIMIT ? OFFSET ?", record.NewInt64(0), record.NewInt64(math.MaxInt64), record.NewInt64(1), record.NewInt64(1))
		require.Equal(t, [][]record.Value{{record.NewInt64(4)}}, result.Rows)
		require.Equal(t, [][]record.Value{{record.NewInt64(3)}}, execute(t, engine, "SELECT COUNT(*) FROM t WHERE score>=9007199254740992").Rows)
		require.Equal(t, [][]record.Value{{record.NewInt64(0)}}, execute(t, engine, "SELECT COUNT(*) FROM t WHERE score>?", record.NewNull()).Rows)
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

func TestSQLComparisonAccessPaths(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,token BIGINT UNIQUE,bucket BIGINT,score BIGINT,INDEX by_bucket(bucket))")
	execute(t, engine, "INSERT INTO t(id,token,bucket,score) VALUES(1,10,1,5)")

	for _, test := range []struct{ where, path string }{
		{"id>0", "primary_range"},
		{"token>=10", "range:1"},
		{"bucket>0", "range:2"},
		{"score>0 AND id=1", "primary"},
		{"id>0 AND id=1", "primary"},
		{"id=1 AND id>=1", "primary"},
		{"token>0 AND token=10", "unique:1"},
		{"score>0 AND bucket=1", "scan:2"},
		{"bucket>0 AND bucket=1", "scan:2"},
		{"token IS NOT NULL AND score>0", "table_scan"},
	} {
		plan, err := Prepare(db, "SELECT id FROM t WHERE "+test.where, PrepareOptions{})
		require.NoError(t, err)
		reader := &selectReader{Database: db}
		result, err := plan.Read(reader)
		require.NoError(t, err)
		require.Equal(t, test.path, reader.path, test.where)
		require.Len(t, result.Rows, 1, test.where)
	}

	execute(t, engine, "SELECT id FROM t WHERE id>0")
	execute(t, engine, "SELECT id FROM t WHERE bucket=1 AND score>0")
	require.Equal(t, float64(1), testutil.ToFloat64(engine.metrics.selects.WithLabelValues("primary_range", "ok")))
	require.Equal(t, float64(1), testutil.ToFloat64(engine.metrics.selects.WithLabelValues("index_scan", "ok")))
}

func TestSQLComparisonDescriptionAndValidation(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,u BIGINT UNSIGNED,active BOOL)")
	before := db.Stats().Writes
	description, err := Describe(db, "SELECT COUNT(*) FROM t WHERE u>=? AND id<? AND active>? LIMIT ? OFFSET ?")
	require.NoError(t, err)
	require.Len(t, description.Parameters, 5)

	for i, typ := range []record.Type{record.TypeUint64, record.TypeInt64, record.TypeBool, record.TypeUint64, record.TypeUint64} {
		require.Equal(t, typ, description.Parameters[i].Type)
		require.Equal(t, i < 3, description.Parameters[i].Nullable)
	}

	for _, test := range []struct {
		query  string
		args   []record.Value
		target error
	}{
		{"SELECT id FROM t WHERE u>-1", nil, ErrType},
		{"SELECT id FROM t WHERE active>2", nil, ErrType},
		{"SELECT id FROM t WHERE id>9223372036854775808", nil, ErrType},
		{"SELECT id FROM t WHERE id>'1'", nil, ErrType},
		{"SELECT id FROM t WHERE id>?", nil, ErrBinding},
		{"SELECT id FROM t WHERE id>?", []record.Value{record.NewUint64(math.MaxUint64)}, ErrType},
		{"SELECT id FROM t WHERE id>NULL AND missing<1", nil, ErrColumn},
		{"SELECT id FROM t WHERE id>NULL AND active>'bad'", nil, ErrType},
		{"SELECT id FROM t WHERE id>NULL LIMIT -1", nil, ErrSyntax},
	} {
		_, err := engine.Execute(context.Background(), test.query, test.args...)
		require.ErrorIs(t, err, test.target, test.query)
	}

	for _, query := range []string{
		"SELECT id FROM t WHERE id < = 1", "SELECT id FROM t WHERE id > = 1", "SELECT id FROM t WHERE id<>1", "SELECT id FROM t WHERE id!=1",
		"SELECT id FROM t WHERE id<=>1", "SELECT id FROM t WHERE id>>1", "SELECT id FROM t WHERE id>=", "SELECT id FROM t WHERE id<id",
		"SELECT id FROM t WHERE id<1.5", "SELECT id FROM t WHERE id<1 OR id>2", "SELECT id FROM t WHERE id BETWEEN 1 AND 2",
		"UPDATE t SET active=TRUE WHERE id>1", "DELETE FROM t WHERE id<=1",
	} {
		_, err := Parse(query)
		require.ErrorIs(t, err, ErrSyntax, query)
	}

	_, err = Parse("SELECT id FROM t WHERE " + strings.Repeat("id>=? AND ", MaxPredicates-1) + "id<?")
	require.NoError(t, err)
	_, err = Parse("SELECT id FROM t WHERE " + strings.Repeat("id>=? AND ", MaxPredicates) + "id<?")
	require.ErrorIs(t, err, ErrLimit)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = engine.Execute(ctx, "SELECT id FROM t WHERE id>1")
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, before, db.Stats().Writes)
}

func TestSQLComparisonCandidateBound(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,bucket BIGINT,INDEX by_bucket(bucket))")

	for id := range int64(MaxSelectRows + 1) {
		execute(t, engine, "INSERT INTO t(id,bucket) VALUES(?,1)", record.NewInt64(id))
	}

	for _, query := range []string{
		"SELECT id FROM t WHERE id>=0 LIMIT 1", "SELECT COUNT(*) FROM t WHERE bucket>=1",
		"SELECT id FROM t WHERE bucket=1 AND id>=0", "SELECT id FROM t WHERE id>=0 LIMIT 0",
	} {
		result, err := engine.Execute(context.Background(), query)
		require.ErrorIs(t, err, ErrLimit, query)
		require.Equal(t, Result{}, result)
	}

	require.Zero(t, testutil.ToFloat64(engine.metrics.rows))
	require.Len(t, execute(t, engine, "SELECT id FROM t WHERE id=1024 AND id>1023").Rows, 1)
}

func TestSQLComparisonNativeBytesAndUUID(t *testing.T) {
	db, _ := openSQLDatabase(t)
	table := schema.Table{ID: 1, Name: "native", PrimaryKeyColumnID: 1, Columns: []schema.Column{{ID: 1, Name: "id", Type: record.TypeUint64}, {ID: 2, Name: "payload", Type: record.TypeBytes}, {ID: 3, Name: "uuid", Type: record.TypeUUID}}}
	require.NoError(t, db.CreateTable(table))
	low, err := record.NewUUID("00000000-0000-0000-0000-000000000001")
	require.NoError(t, err)
	high, err := record.NewUUID("ffffffff-ffff-ffff-ffff-ffffffffffff")
	require.NoError(t, err)
	require.NoError(t, db.InsertRow(table, schema.Row{1: record.NewUint64(1), 2: record.NewBytes([]byte{0, 255}), 3: low}))
	require.NoError(t, db.InsertRow(table, schema.Row{1: record.NewUint64(2), 2: record.NewBytes([]byte{1, 0}), 3: high}))
	engine, err := NewLocal(db)
	require.NoError(t, err)
	result := execute(t, engine, "SELECT id FROM native WHERE payload>? AND uuid>=?", record.NewBytes([]byte{0, 255}), high)
	require.Equal(t, [][]record.Value{{record.NewUint64(2)}}, result.Rows)
}
