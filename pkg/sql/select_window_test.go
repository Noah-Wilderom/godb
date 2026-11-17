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

func TestSQLSelectOrderAndLimit(t *testing.T) {
	db, path := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE ordered(id BIGINT PRIMARY KEY,bucket BIGINT,score BIGINT,rank BIGINT UNSIGNED,label TEXT,active BOOL,KEY bucket_idx(bucket))")

	for i, row := range [][]record.Value{
		{record.NewInt64(10), record.NewUint64(math.MaxUint64), textValue(t, "a"), record.NewBool(true)},
		{record.NewInt64(2), record.NewUint64(2), textValue(t, "Z"), record.NewBool(false)},
		{record.NewNull(), record.NewNull(), textValue(t, ""), record.NewNull()},
		{record.NewInt64(math.MinInt64), record.NewUint64(0), textValue(t, "a"), record.NewBool(false)},
		{record.NewInt64(2), record.NewUint64(math.MaxUint64 - 1), record.NewNull(), record.NewBool(true)},
		{record.NewInt64(math.MaxInt64), record.NewUint64(10), textValue(t, "a\x00"), record.NewBool(false)},
	} {
		args := append([]record.Value{record.NewInt64(int64(i + 1))}, row...)
		execute(t, engine, "INSERT INTO ordered(id,bucket,score,rank,label,active) VALUES (?,1,?,?,?,?)", args...)
	}

	check := func(engine *LocalEngine) {
		t.Helper()

		for _, test := range []struct {
			suffix string
			args   []record.Value
			ids    []int64
		}{
			{"ORDER BY score", nil, []int64{3, 4, 2, 5, 1, 6}},
			{"ORDER BY score DESC,id DESC", nil, []int64{6, 1, 5, 2, 4, 3}},
			{"ORDER BY rank ASC", nil, []int64{3, 4, 2, 6, 5, 1}},
			{"ORDER BY rank DESC", nil, []int64{1, 5, 6, 2, 4, 3}},
			{"ORDER BY label,id", nil, []int64{5, 3, 2, 1, 4, 6}},
			{"ORDER BY active,id", nil, []int64{3, 2, 4, 6, 1, 5}},
			{"ORDER BY active DESC,id", nil, []int64{1, 5, 2, 4, 6, 3}},
			{"ORDER BY id DESC LIMIT 2", nil, []int64{6, 5}},
			{"ORDER BY score DESC,id DESC LIMIT 2 OFFSET 1", nil, []int64{1, 5}},
			{"ORDER BY score DESC,id DESC LIMIT 1,2", nil, []int64{1, 5}},
			{"ORDER BY id DESC LIMIT ? OFFSET ?", []record.Value{record.NewInt64(2), record.NewUint64(1)}, []int64{5, 4}},
			{"ORDER BY id DESC LIMIT ?,?", []record.Value{record.NewInt64(1), record.NewUint64(2)}, []int64{5, 4}},
			{"ORDER BY id DESC LIMIT 0", nil, nil},
			{"ORDER BY id LIMIT 1024 OFFSET 1024", nil, nil},
			{"ORDER BY id LIMIT 4 OFFSET 4", nil, []int64{5, 6}},
			{"ORDER BY id,id DESC", nil, []int64{1, 2, 3, 4, 5, 6}},
			{"LIMIT 2", nil, []int64{1, 2}},
		} {
			result := execute(t, engine, "SELECT id FROM ordered WHERE bucket=1 "+test.suffix, test.args...)

			want := make([][]record.Value, 0, len(test.ids))
			for _, id := range test.ids {
				want = append(want, []record.Value{record.NewInt64(id)})
			}

			require.Equal(t, want, result.Rows, test.suffix)
			require.Len(t, result.Columns, 1)
		}

		result := execute(t, engine, "SELECT id FROM ordered WHERE bucket=1 AND active=TRUE ORDER BY score DESC LIMIT 1 OFFSET 1")
		require.Equal(t, [][]record.Value{{record.NewInt64(5)}}, result.Rows)
		require.Empty(t, execute(t, engine, "SELECT id FROM ordered WHERE id=1 ORDER BY score LIMIT 1 OFFSET 1").Rows)
		require.Empty(t, execute(t, engine, "SELECT id FROM ordered WHERE bucket=9 ORDER BY score LIMIT 1").Rows)
	}
	before := db.Stats().Writes
	check(engine)
	require.Equal(t, before, db.Stats().Writes)
	require.NoError(t, db.Close())
	reopened, err := database.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	engine, err = NewLocal(reopened)
	require.NoError(t, err)
	check(engine)
}

func TestSQLSelectWindowValidationAndDescribe(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE users(id BIGINT PRIMARY KEY,name TEXT)")
	before := db.Stats().Writes

	for _, test := range []struct {
		suffix string
		args   []record.Value
		target error
	}{
		{"ORDER BY missing", nil, ErrColumn},
		{"LIMIT 1025", nil, ErrLimit},
		{"LIMIT 1 OFFSET 1025", nil, ErrLimit},
		{"LIMIT 18446744073709551616", nil, ErrType},
		{"LIMIT ?", []record.Value{record.NewInt64(-1)}, ErrType},
		{"LIMIT ?", []record.Value{record.NewUint64(math.MaxUint64)}, ErrLimit},
		{"LIMIT ?", []record.Value{record.NewNull()}, ErrType},
		{"LIMIT ?", []record.Value{textValue(t, "1")}, ErrType},
		{"LIMIT ?", []record.Value{record.NewBool(true)}, ErrType},
		{"LIMIT ?", []record.Value{record.NewFloat64(1)}, ErrType},
		{"LIMIT 1 OFFSET ?", []record.Value{record.NewNull()}, ErrType},
		{"LIMIT ? OFFSET ?", []record.Value{record.NewInt64(1)}, ErrBinding},
	} {
		_, err := engine.Execute(context.Background(), "SELECT * FROM users WHERE id=1 "+test.suffix, test.args...)
		require.ErrorIs(t, err, test.target, test.suffix)
	}

	for _, suffix := range []string{"LIMIT ? OFFSET ?", "LIMIT ?,?"} {
		description, err := Describe(db, "SELECT name FROM users WHERE id=? ORDER BY id DESC "+suffix)
		require.NoError(t, err)
		require.Len(t, description.Parameters, 3)
		require.Equal(t, record.TypeInt64, description.Parameters[0].Type)

		for _, parameter := range description.Parameters[1:] {
			require.Equal(t, record.TypeUint64, parameter.Type)
			require.False(t, parameter.Nullable)
		}

		if suffix == "LIMIT ?,?" {
			require.Equal(t, "offset", description.Parameters[1].Name)
		} else {
			require.Equal(t, "limit", description.Parameters[1].Name)
		}

		require.Equal(t, "name", description.Columns[0].Name)
	}

	_, err = Describe(db, "SELECT id FROM users WHERE id=? LIMIT 1025")
	require.ErrorIs(t, err, ErrLimit)
	require.Equal(t, before, db.Stats().Writes)
}

func TestSQLSelectWindowSyntaxAndBounds(t *testing.T) {
	for _, suffix := range []string{
		"ORDER id", "ORDER BY", "ORDER BY id,", "ORDER BY 1", "ORDER BY ?", "ORDER BY id ASC DESC",
		"ORDER BY id NULLS FIRST", "ORDER BY id+1", "LIMIT", "LIMIT -1", "LIMIT +1", "LIMIT NULL", "LIMIT TRUE",
		"LIMIT '1'", "LIMIT 1.5", "LIMIT 1,", "LIMIT 1 OFFSET", "LIMIT 1,2 OFFSET 3",
		"LIMIT 1 OFFSET 2,3", "OFFSET 1", "LIMIT 1 ORDER BY id", "ORDER BY id ORDER BY id",
	} {
		_, err := Parse("SELECT id FROM users WHERE id=1 " + suffix)
		require.ErrorIs(t, err, ErrSyntax, suffix)
	}

	query := "SELECT id FROM users WHERE id=1 ORDER BY " + strings.Repeat("id,", MaxOrderColumns-1) + "id"
	_, err := Parse(query)
	require.NoError(t, err)
	_, err = Parse(query + ",id")
	require.ErrorIs(t, err, ErrLimit)
}

func TestSQLSelectLimitDoesNotBypassScanBound(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE many(id BIGINT PRIMARY KEY,bucket BIGINT,KEY bucket_idx(bucket))")

	for id := range int64(MaxSelectRows + 1) {
		execute(t, engine, "INSERT INTO many(id,bucket) VALUES (?,1)", record.NewInt64(id))
	}

	for _, suffix := range []string{"LIMIT 1", "ORDER BY id DESC LIMIT 1", "ORDER BY id LIMIT 0"} {
		result, err := engine.Execute(context.Background(), "SELECT id FROM many WHERE bucket=1 "+suffix)
		require.ErrorIs(t, err, ErrLimit)
		require.Empty(t, result.Rows)
	}

	require.Zero(t, testutil.ToFloat64(engine.metrics.rows))
}

func TestSQLSortNativeKeysAndInvalidRows(t *testing.T) {
	small, err := record.NewUUID("00000000-0000-0000-0000-000000000001")
	require.NoError(t, err)
	large, err := record.NewUUID("ffffffff-ffff-ffff-ffff-ffffffffffff")
	require.NoError(t, err)

	for _, values := range [][]record.Value{{record.NewBytes([]byte{0}), record.NewBytes([]byte{1})}, {small, large}} {
		plan := Plan{orders: []sortColumn{{column: schema.Column{ID: 1, Type: values[0].Type()}}}}
		rows := []schema.Row{{1: values[1]}, {1: record.NewNull()}, {1: values[0]}}
		require.NoError(t, plan.sortRows(rows))
		require.Equal(t, []schema.Row{{1: record.NewNull()}, {1: values[0]}, {1: values[1]}}, rows)
		require.ErrorIs(t, plan.sortRows([]schema.Row{{}}), database.ErrInvalidData)
		require.ErrorIs(t, plan.sortRows([]schema.Row{{1: record.NewBool(true)}}), database.ErrInvalidData)
	}
}
