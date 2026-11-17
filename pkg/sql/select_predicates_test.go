package sql

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
)

func TestSQLSelectMultiplePredicates(t *testing.T) {
	db, path := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE accounts(id BIGINT PRIMARY KEY,tenant BIGINT UNSIGNED,handle TEXT,active BOOL,note TEXT,UNIQUE KEY identity(tenant,handle),KEY handles(handle))")

	for _, query := range []string{
		"INSERT INTO accounts(id,tenant,handle,active,note) VALUES(1,7,'alice',TRUE,'yes')",
		"INSERT INTO accounts(id,tenant,handle,active,note) VALUES(2,8,'alice',FALSE,'no')",
		"INSERT INTO accounts(id,tenant,handle,active,note) VALUES(3,7,'bob',NULL,'yes')",
		"INSERT INTO accounts(id,tenant,handle,active,note) VALUES(4,7,NULL,TRUE,NULL)",
	} {
		execute(t, engine, query)
	}

	check := func(engine *LocalEngine) {
		t.Helper()

		for _, test := range []struct {
			where string
			args  []record.Value
			ids   []int64
		}{
			{"handle=? AND tenant=?", []record.Value{textValue(t, "alice"), record.NewUint64(7)}, []int64{1}},
			{"tenant=7 AND handle='alice'", nil, []int64{1}},
			{"tenant=7 AND active=TRUE", nil, []int64{1, 4}},
			{"tenant=7 AND active=FALSE", nil, nil},
			{"tenant=7 AND note='yes'", nil, []int64{1, 3}},
			{"id=1 AND active=1 AND handle='alice'", nil, []int64{1}},
			{"id=1 AND active=FALSE", nil, nil},
			{"tenant=7 AND tenant=7", nil, []int64{1, 3, 4}},
			{"tenant=7 AND tenant=8", nil, nil},
			{"tenant=7 AND handle='alice' AND handle='bob'", nil, nil},
			{"tenant=7 AND active=NULL", nil, nil},
			{"tenant=NULL AND handle='alice'", nil, nil},
			{"handle='ALICE' AND tenant=7", nil, nil},
		} {
			result := execute(t, engine, "SELECT id FROM accounts WHERE "+test.where, test.args...)

			want := make([][]record.Value, 0, len(test.ids))
			for _, id := range test.ids {
				want = append(want, []record.Value{record.NewInt64(id)})
			}

			require.ElementsMatch(t, want, result.Rows, test.where)
			require.Len(t, result.Columns, 1)
		}
	}
	check(engine)

	for _, test := range []struct {
		query  string
		args   []record.Value
		target error
	}{
		{"SELECT * FROM accounts WHERE tenant=7 AND absent=1", nil, ErrColumn},
		{"SELECT * FROM accounts WHERE tenant=NULL AND active='bad'", nil, ErrType},
		{"SELECT * FROM accounts WHERE tenant=7 AND active=?", []record.Value{record.NewInt64(2)}, ErrType},
		{"SELECT * FROM accounts WHERE handle=? AND tenant=?", []record.Value{record.NewUint64(7), textValue(t, "alice")}, ErrType},
		{"SELECT * FROM accounts WHERE handle=? AND tenant=?", []record.Value{textValue(t, "alice")}, ErrBinding},
	} {
		_, err := engine.Execute(context.Background(), test.query, test.args...)
		require.ErrorIs(t, err, test.target, test.query)
	}

	description, err := Describe(db, "SELECT id,active FROM accounts WHERE handle=? AND tenant=? AND active=? AND tenant=?")
	require.NoError(t, err)
	require.Len(t, description.Parameters, 4)

	for i, typ := range []record.Type{record.TypeText, record.TypeUint64, record.TypeBool, record.TypeUint64} {
		require.Equal(t, typ, description.Parameters[i].Type)
		require.True(t, description.Parameters[i].Nullable)
	}

	require.NoError(t, db.Close())
	reopened, err := database.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	engine, err = NewLocal(reopened)
	require.NoError(t, err)
	check(engine)
}

func TestSQLSelectCompositeAccessPaths(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE paths(id BIGINT PRIMARY KEY,a BIGINT,b BIGINT,c BIGINT,d BIGINT,KEY a_only(a),KEY abc(a,b,c),KEY ab(a,b),UNIQUE KEY cd(c,d))")
	execute(t, engine, "INSERT INTO paths(id,a,b,c,d) VALUES(1,2,3,4,5)")

	for _, test := range []struct{ where, path string }{
		{"a=2 AND b=3", "scan:3"},
		{"b=3 AND a=2", "scan:3"},
		{"a=2 AND b=3 AND c=4", "scan:2"},
		{"a=2 AND c=4", "scan:1"},
		{"a=2 AND b=3 AND c=4 AND d=5", "unique:4"},
		{"d=5 AND c=4", "unique:4"},
		{"id=1 AND c=4 AND d=5", "primary"},
		{"c=4", "scan:4"},
	} {
		plan, err := Prepare(db, "SELECT * FROM paths WHERE "+test.where, PrepareOptions{})
		require.NoError(t, err)
		reader := &selectReader{Database: db}
		result, err := plan.Read(reader)
		require.NoError(t, err)
		require.Len(t, result.Rows, 1)
		require.Equal(t, test.path, reader.path, test.where)
	}

	execute(t, engine, "INSERT INTO paths(id,a,b,c,d) VALUES(2,2,3,9,8)")
	require.Len(t, execute(t, engine, "SELECT id FROM paths WHERE b=3 AND a=2").Rows, 2)
	require.Equal(t, [][]record.Value{{record.NewInt64(1)}}, execute(t, engine, "SELECT id FROM paths WHERE d=5 AND a=2").Rows)
}

func TestSQLSelectPredicateBounds(t *testing.T) {
	query := "SELECT * FROM t WHERE " + strings.Repeat("id=? AND ", MaxPredicates-1) + "id=?"
	statement, err := Parse(query)
	require.NoError(t, err)
	require.Len(t, statement.predicates, MaxPredicates)
	require.Equal(t, MaxPredicates, statement.parameters)
	_, err = Parse(query + " AND id=?")
	require.ErrorIs(t, err, ErrLimit)

	for _, query := range []string{
		"SELECT * FROM t WHERE id=1 AND", "SELECT * FROM t WHERE AND id=1",
		"SELECT * FROM t WHERE id=1 AND AND id=2", "SELECT * FROM t WHERE id=1 AND x>y",
		"SELECT * FROM t WHERE id=1 AND (x=2)", "SELECT * FROM t WHERE id=1 OR x=2",
		"SELECT * FROM t WHERE id=1 AND x=2 OR x=3",
	} {
		_, err := Parse(query)
		require.ErrorIs(t, err, ErrSyntax, query)
	}
}

func TestSQLSelectResidualScanBound(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE many(id BIGINT PRIMARY KEY,a BIGINT,b BIGINT,active BOOL,KEY ab(a,b))")

	for id := range int64(MaxSelectRows + 1) {
		execute(t, engine, "INSERT INTO many(id,a,b,active) VALUES (?,1,?,TRUE)", record.NewInt64(id), record.NewInt64(id))
	}

	result, err := engine.Execute(context.Background(), "SELECT id FROM many WHERE a=1 AND active=FALSE")
	require.ErrorIs(t, err, ErrLimit)
	require.Empty(t, result.Rows)
	require.Equal(t, [][]record.Value{{record.NewInt64(3)}}, execute(t, engine, "SELECT id FROM many WHERE b=3 AND a=1").Rows)
}

func TestEqualityMatchesScalarTypes(t *testing.T) {
	for _, value := range []record.Value{
		record.NewInt64(math.MinInt64), record.NewUint64(math.MaxUint64), record.NewBool(false),
		textValue(t, "a\x00b"), record.NewBytes([]byte{0, 1, 2}),
	} {
		require.True(t, equalityMatches(value, value))
		require.False(t, equalityMatches(value, record.NewNull()))
	}

	require.False(t, equalityMatches(record.NewInt64(1), record.NewUint64(1)))
	require.False(t, equalityMatches(record.NewBytes([]byte{1}), record.NewBytes([]byte{2})))
	require.False(t, equalityMatches(record.NewNull(), record.NewNull()))
}
