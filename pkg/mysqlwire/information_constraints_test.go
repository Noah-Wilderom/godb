package mysqlwire

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func constraintSession(t testing.TB) *statementSession {
	t.Helper()
	s, _ := discoverySession(t)
	table := discoveryFixture()
	table.Indexes[1].Unique = true
	table.Indexes = append(table.Indexes, schema.Index{ID: 3, Name: "lookup", ColumnIDs: []uint32{2}})
	s.endpoint.query.Catalog = func(context.Context, string) ([]schema.Table, error) { return []schema.Table{table}, nil }
	return s
}

func TestInformationConstraintsAndKeyOrder(t *testing.T) {
	s := constraintSession(t)
	ctx := context.Background()
	result, err := s.query(ctx, "SELECT CONSTRAINT_NAME,CONSTRAINT_TYPE,ENFORCED FROM information_schema.table_constraints WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='items' ORDER BY CONSTRAINT_NAME")
	require.NoError(t, err)
	require.Equal(t, [][]record.Value{
		{infoText(t, "PRIMARY"), infoText(t, "PRIMARY KEY"), infoText(t, "YES")},
		{infoText(t, "active_email"), infoText(t, "UNIQUE"), infoText(t, "YES")},
		{infoText(t, "email_unique"), infoText(t, "UNIQUE"), infoText(t, "YES")},
	}, result.Rows)
	query := "SELECT CONSTRAINT_NAME,COLUMN_NAME,ORDINAL_POSITION,POSITION_IN_UNIQUE_CONSTRAINT,REFERENCED_TABLE_SCHEMA,REFERENCED_TABLE_NAME,REFERENCED_COLUMN_NAME FROM information_schema.key_column_usage WHERE CONSTRAINT_NAME=? ORDER BY ORDINAL_POSITION"
	description, err := s.describe(ctx, query)
	require.NoError(t, err)
	require.Len(t, description.Parameters, 1)
	require.Equal(t, record.TypeText, description.Parameters[0].Type)
	require.Equal(t, record.TypeInt64, description.Columns[3].Type)
	require.True(t, description.Columns[3].Nullable)
	result, err = s.query(ctx, query, infoText(t, "active_email"))
	require.NoError(t, err)
	require.Equal(t, description.Columns, result.Columns)
	require.Equal(t, [][]record.Value{
		{infoText(t, "active_email"), infoText(t, "active"), record.NewInt64(1), record.NewNull(), record.NewNull(), record.NewNull(), record.NewNull()},
		{infoText(t, "active_email"), infoText(t, "email"), record.NewInt64(2), record.NewNull(), record.NewNull(), record.NewNull(), record.NewNull()},
	}, result.Rows)

	for _, name := range []string{"lookup", "missing", "PRIMARY' OR 1=1 --"} {
		result, err := s.query(ctx, query, infoText(t, name))
		require.NoError(t, err)
		require.Empty(t, result.Rows)
	}

	for _, entry := range []struct {
		view, where string
		count       int64
	}{
		{"TABLE_CONSTRAINTS", "CONSTRAINT_TYPE='UNIQUE'", 2},
		{"TABLE_CONSTRAINTS", "CONSTRAINT_TYPE='FOREIGN KEY'", 0},
		{"TABLE_CONSTRAINTS", "CONSTRAINT_TYPE='CHECK'", 0},
		{"KEY_COLUMN_USAGE", "POSITION_IN_UNIQUE_CONSTRAINT IS NULL", 4},
		{"KEY_COLUMN_USAGE", "REFERENCED_TABLE_NAME IS NOT NULL", 0},
		{"KEY_COLUMN_USAGE", "CONSTRAINT_NAME='PRIMARY' AND ORDINAL_POSITION=1", 1},
	} {
		result, err := s.query(ctx, "SELECT COUNT(*) FROM information_schema."+entry.view+" WHERE "+entry.where)
		require.NoError(t, err)
		require.Equal(t, record.NewInt64(entry.count), result.Rows[0][0])
	}

	result, err = s.query(ctx, "SELECT * FROM information_schema.key_column_usage WHERE TABLE_SCHEMA='missing'")
	require.NoError(t, err)
	require.Empty(t, result.Rows)
}

func TestConstraintTypesDoNotDependOnIndexNames(t *testing.T) {
	s := constraintSession(t)
	table := discoveryFixture()
	table.Indexes[0].Name = "PRIMARY"
	s.endpoint.query.Catalog = func(context.Context, string) ([]schema.Table, error) { return []schema.Table{table}, nil }
	result, err := s.query(context.Background(), "SELECT CONSTRAINT_TYPE FROM information_schema.table_constraints ORDER BY CONSTRAINT_TYPE")
	require.NoError(t, err)
	require.Equal(t, [][]record.Value{{infoText(t, "PRIMARY KEY")}, {infoText(t, "UNIQUE")}}, result.Rows)
}

func BenchmarkInformationConstraints(b *testing.B) {
	s := constraintSession(b)
	query := "SELECT CONSTRAINT_NAME,COLUMN_NAME,ORDINAL_POSITION FROM information_schema.key_column_usage WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='items' ORDER BY CONSTRAINT_NAME,ORDINAL_POSITION"
	b.ReportAllocs()

	for b.Loop() {
		_, err := s.query(context.Background(), query)
		if err != nil {
			b.Fatal(err)
		}
	}
}
