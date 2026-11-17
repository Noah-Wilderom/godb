package sql

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
)

func TestSQLExplainPlans(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,token TEXT UNIQUE,tenant BIGINT,score BIGINT,active BOOL,INDEX by_score(tenant,score))")

	for _, test := range []struct {
		query, access, index, rangeColumn, lower, upper string
		prefix, cap                                     int64
	}{
		{"SELECT id FROM t", "table_scan", "", "", "", "", 0, 1024},
		{"SELECT id FROM t WHERE id=1", "primary", "PRIMARY", "", "", "", 1, 1},
		{"SELECT id FROM t WHERE token='one'", "unique", "token", "", "", "", 1, 1},
		{"SELECT id FROM t WHERE id>1 AND id<=10", "primary_range", "PRIMARY", "id", "exclusive", "inclusive", 0, 1024},
		{"SELECT id FROM t WHERE id>=1 AND id>1 AND id<10", "primary_range", "PRIMARY", "id", "exclusive", "exclusive", 0, 1024},
		{"SELECT id FROM t WHERE id<=1", "primary_range", "PRIMARY", "id", "unbounded", "inclusive", 0, 1024},
		{"SELECT id FROM t WHERE tenant=7", "index_scan", "by_score", "", "", "", 1, 1024},
		{"SELECT id FROM t WHERE tenant=7 AND score>=50", "index_range", "by_score", "score", "inclusive", "unbounded", 1, 1024},
		{"SELECT id FROM t WHERE tenant IS NULL AND score<50", "index_range", "by_score", "score", "unbounded", "exclusive", 1, 1024},
		{"SELECT id FROM t WHERE token>'a'", "index_range", "token", "token", "exclusive", "unbounded", 0, 1024},
		{"SELECT id FROM t WHERE score>50", "table_scan", "", "", "", "", 0, 1024},
		{"SELECT id FROM t WHERE id=NULL", "primary", "PRIMARY", "", "", "", 1, 0},
		{"SELECT id FROM t WHERE id IS NULL", "primary", "PRIMARY", "", "", "", 1, 0},
		{"SELECT id FROM t WHERE token IS NULL", "index_scan", "token", "", "", "", 1, 1024},
		{"SELECT id FROM t WHERE tenant=7 AND score>=50 AND id=1", "primary", "PRIMARY", "", "", "", 1, 1},
		{"SELECT id FROM t WHERE tenant=7 AND score>=50 AND id>1", "primary_range", "PRIMARY", "id", "exclusive", "unbounded", 0, 1024},
	} {
		t.Run(test.query, func(t *testing.T) {
			plan, err := Prepare(db, "EXPLAIN "+test.query, PrepareOptions{})
			require.NoError(t, err)
			require.Equal(t, Explain, plan.Kind())
			before := db.Stats()
			result, err := plan.Read(nil)
			require.NoError(t, err)
			require.Equal(t, before, db.Stats())
			row := explainValues(t, result)
			require.Equal(t, textValue(t, "t"), row["table"])
			require.Equal(t, textValue(t, test.access), row["access"])

			for name, want := range map[string]string{"index": test.index, "range_column": test.rangeColumn, "lower_bound": test.lower, "upper_bound": test.upper} {
				if want == "" {
					require.True(t, row[name].IsNull(), name)
				} else {
					require.Equal(t, textValue(t, want), row[name], name)
				}
			}

			require.Equal(t, record.NewInt64(test.prefix), row["equality_prefix_columns"])
			require.Equal(t, record.NewInt64(test.cap), row["candidate_limit"])
			require.Equal(t, record.NewInt64(1024), row["result_limit"])
			require.Equal(t, record.NewInt64(0), row["result_offset"])
			require.Empty(t, plan.AccessPath())
			require.Empty(t, plan.Aggregate())
			_, err = plan.Command()
			require.ErrorIs(t, err, ErrUnsupported)
			_, err = plan.ApplyResult(database.CommandResult{})
			require.ErrorIs(t, err, ErrUnsupported)
			result.Columns[0].Name = "mutated"
			result.Rows[0][0] = record.NewNull()
			again, err := plan.Read(nil)
			require.NoError(t, err)
			require.Equal(t, "table", again.Columns[0].Name)
			require.Equal(t, textValue(t, "t"), again.Rows[0][0])
		})
	}
}

func explainValues(t testing.TB, result Result) map[string]record.Value {
	t.Helper()
	require.Len(t, result.Rows, 1)
	require.Len(t, result.Columns, 11)
	require.Len(t, result.Rows[0], len(result.Columns))

	values := make(map[string]record.Value, len(result.Columns))
	for i, column := range result.Columns {
		values[column.Name] = result.Rows[0][i]
	}

	return values
}

func TestSQLExplainParametersDescriptionAndMetrics(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,tenant BIGINT,score BIGINT,INDEX by_score(tenant,score))")
	query := "SELECT id FROM t WHERE score>=? AND tenant=? ORDER BY score DESC LIMIT ? OFFSET ?"
	description, err := Describe(db, "EXPLAIN "+query)
	require.NoError(t, err)
	selectDescription, err := Describe(db, query)
	require.NoError(t, err)
	require.Equal(t, selectDescription.Parameters, description.Parameters)
	require.Equal(t, explainColumns(), description.Columns)
	before := db.Stats().Writes
	result := execute(t, engine, "EXPLAIN "+query, record.NewInt64(50), record.NewInt64(7), record.NewInt64(0), record.NewInt64(2))
	row := explainValues(t, result)
	require.Equal(t, textValue(t, "index_range"), row["access"])
	require.Equal(t, record.NewInt64(0), row["result_limit"])
	require.Equal(t, record.NewInt64(2), row["result_offset"])
	require.Equal(t, textValue(t, "Recheck 2 predicates; Sort captured rows"), row["extra"])
	result = execute(t, engine, "EXPLAIN SELECT COUNT(score) AS total FROM t WHERE tenant=? LIMIT 0", record.NewNull())
	row = explainValues(t, result)
	require.Equal(t, record.NewInt64(0), row["candidate_limit"])
	require.Equal(t, textValue(t, "No matching rows (NULL predicate); Recheck 1 predicates; COUNT(`score`)"), row["extra"])
	require.Equal(t, before, db.Stats().Writes)
	require.Zero(t, testutil.CollectAndCount(engine.metrics.selects))
	require.Zero(t, testutil.CollectAndCount(engine.metrics.aggregates))
	require.Equal(t, float64(2), testutil.ToFloat64(engine.metrics.rows))
	registry := prometheus.NewPedanticRegistry()
	require.NoError(t, registry.Register(engine.metrics.duration))
	metrics, err := registry.Gather()
	require.NoError(t, err)
	found := false

	for _, family := range metrics {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "statement" && label.GetValue() == "explain" {
					require.Equal(t, uint64(2), metric.GetHistogram().GetSampleCount())
					found = true
				}
			}
		}
	}

	require.True(t, found)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = engine.Execute(ctx, "EXPLAIN "+query)
	require.ErrorIs(t, err, context.Canceled)
}

func TestSQLExplainValidation(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,score BIGINT)")

	for _, query := range []string{"EXPLAIN", "EXPLAIN SELECT", "EXPLAIN ANALYZE SELECT id FROM t", "EXPLAIN FORMAT=JSON SELECT id FROM t", "EXPLAIN UPDATE t SET score=1 WHERE id=1", "EXPLAIN DELETE FROM t WHERE id=1", "EXPLAIN CREATE TABLE a(id BIGINT PRIMARY KEY)", "EXPLAIN EXPLAIN SELECT id FROM t", "EXPLAIN SELECT id FROM t; DELETE FROM t WHERE id=1"} {
		_, err := Parse(query)
		require.ErrorIs(t, err, ErrSyntax, query)
	}

	for _, test := range []struct {
		query string
		args  []record.Value
		want  error
	}{
		{"EXPLAIN SELECT id FROM missing", nil, database.ErrTableNotFound},
		{"EXPLAIN SELECT missing FROM t", nil, ErrColumn},
		{"EXPLAIN SELECT id FROM t WHERE missing=NULL", nil, ErrColumn},
		{"EXPLAIN SELECT COUNT(*) FROM t ORDER BY id", nil, ErrUnsupported},
		{"EXPLAIN SELECT id FROM t LIMIT 1025", nil, ErrLimit},
		{"EXPLAIN SELECT id FROM t WHERE score=?", nil, ErrBinding},
		{"EXPLAIN SELECT id FROM t WHERE score=?", []record.Value{textValue(t, "wrong")}, ErrType},
	} {
		_, err := engine.Execute(context.Background(), test.query, test.args...)
		require.ErrorIs(t, err, test.want, test.query)
	}

	_, err = Prepare(db, "EXPLAIN SELECT id FROM t", PrepareOptions{CreateTableID: 1})
	require.ErrorIs(t, err, ErrBinding)
}

func BenchmarkSQLExplain(b *testing.B) {
	db, _ := openSQLDatabase(b)
	engine, err := NewLocal(db)
	require.NoError(b, err)
	execute(b, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,tenant BIGINT,score BIGINT,INDEX by_score(tenant,score))")
	query := "EXPLAIN SELECT id FROM t WHERE tenant=? AND score>=? ORDER BY score LIMIT 20"
	args := []record.Value{record.NewInt64(7), record.NewInt64(50)}
	plan, err := Prepare(db, query, PrepareOptions{}, args...)
	require.NoError(b, err)
	b.Run("prepared", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			if _, err := plan.Read(nil); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("local", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			if _, err := engine.Execute(context.Background(), query, args...); err != nil {
				b.Fatal(err)
			}
		}
	})
}
