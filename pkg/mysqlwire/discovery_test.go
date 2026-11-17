package mysqlwire

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
	"github.com/Noah-Wilderom/godb/pkg/sql"
)

func discoveryFixture() schema.Table {
	return schema.Table{
		ID: 1, Name: "items", PrimaryKeyColumnID: 1,
		Columns: []schema.Column{{ID: 1, Name: "id", Type: record.TypeUint64}, {ID: 2, Name: "email", Type: record.TypeText, Nullable: true}, {ID: 3, Name: "active", Type: record.TypeBool}, {ID: 4, Name: "extra", Type: record.TypeUUID, Nullable: true}},
		Indexes: []schema.Index{{ID: 1, Name: "email_unique", ColumnIDs: []uint32{2}, Unique: true}, {ID: 2, Name: "active_email", ColumnIDs: []uint32{3, 2}}},
	}
}

func discoverySession(t testing.TB) (*statementSession, *int) {
	t.Helper()
	s, _, _, _ := newTestSession(t)
	reads := new(int)
	s.endpoint.query.Catalog = func(_ context.Context, name string) ([]schema.Table, error) {
		*reads++

		if name != "" && name != "items" {
			return nil, &Error{1146, "42S02", "Table does not exist"}
		}

		return []schema.Table{discoveryFixture()}, nil
	}
	return s, reads
}

func textCell(t testing.TB, value record.Value) string {
	t.Helper()
	text, err := value.Text()
	require.NoError(t, err)
	return text
}

func TestDiscoveryReflectsCatalog(t *testing.T) {
	s, _ := discoverySession(t)
	ctx := context.Background()
	columns, err := s.query(ctx, "SHOW FULL COLUMNS IN `godb`.`items`")
	require.NoError(t, err)
	require.Len(t, columns.Rows, 4)
	require.Equal(t, "bigint unsigned", textCell(t, columns.Rows[0][1]))
	require.Equal(t, "PRI", textCell(t, columns.Rows[0][4]))
	require.Equal(t, "UNI", textCell(t, columns.Rows[1][4]))
	require.Equal(t, "MUL", textCell(t, columns.Rows[2][4]))
	require.True(t, columns.Rows[0][2].IsNull())
	require.True(t, columns.Rows[0][5].IsNull())
	require.Equal(t, "utf8mb4_bin", textCell(t, columns.Rows[1][2]))
	require.Equal(t, "YES", textCell(t, columns.Rows[1][3]))
	require.Equal(t, "godb_uuid", textCell(t, columns.Rows[3][1]))
	indexes, err := s.query(ctx, "SHOW INDEXES FROM items FROM godb")
	require.NoError(t, err)
	require.Len(t, indexes.Rows, 4)
	require.Equal(t, "PRIMARY", textCell(t, indexes.Rows[0][2]))
	require.Equal(t, record.NewInt64(0), indexes.Rows[1][1])
	require.Equal(t, record.NewInt64(1), indexes.Rows[2][1])
	require.Equal(t, "active_email", textCell(t, indexes.Rows[3][2]))
	require.Equal(t, record.NewInt64(2), indexes.Rows[3][3])
	require.Equal(t, "email", textCell(t, indexes.Rows[3][4]))

	for _, row := range indexes.Rows {
		require.True(t, row[6].IsNull())
		require.True(t, row[7].IsNull())
		require.True(t, row[8].IsNull())
		require.True(t, row[14].IsNull())
		require.Equal(t, "BTREE", textCell(t, row[10]))
	}

	for _, query := range []string{"SHOW COLUMNS FROM items", "DESCRIBE items", "DESC godb.items", "SHOW FIELDS IN items IN godb"} {
		result, err := s.query(ctx, query)
		require.NoError(t, err)
		require.Len(t, result.Columns, 6)
		require.Len(t, result.Rows, 4)
		description, err := s.describe(ctx, query)
		require.NoError(t, err)
		require.Equal(t, result.Columns, description.Columns)
		_, err = resultPackets("godb", result)
		require.NoError(t, err)
		_, err = binaryResultPackets("godb", result)
		require.NoError(t, err)
	}
}

func TestDiscoveryFilteringBindingAndBounds(t *testing.T) {
	s, reads := discoverySession(t)
	ctx := context.Background()
	result, err := s.query(ctx, "SHOW DATABASES")
	require.NoError(t, err)
	require.Len(t, result.Rows, 1)
	require.Zero(t, *reads)

	for _, entry := range []struct {
		query string
		count int
	}{
		{"SHOW FULL TABLES FROM godb LIKE 'it_ms'", 1},
		{"SHOW TABLES LIKE 'ITEMS'", 0},
		{"DESCRIBE items email", 1},
		{"DESC items 'e%'", 2},
		{"SHOW COLUMNS FROM items LIKE 'no%'", 0},
	} {
		result, err := s.query(ctx, entry.query)
		require.NoError(t, err, entry.query)
		require.Len(t, result.Rows, entry.count)
	}

	pattern, err := record.NewText("e%")
	require.NoError(t, err)
	description, err := s.describe(ctx, "SHOW COLUMNS FROM items LIKE ?")
	require.NoError(t, err)
	require.Len(t, description.Parameters, 1)
	result, err = s.query(ctx, "SHOW COLUMNS FROM items LIKE ?", pattern)
	require.NoError(t, err)
	require.Len(t, result.Rows, 2)
	_, err = s.query(ctx, "SHOW TABLES LIKE ?", record.NewInt64(1))
	require.Error(t, err)
	_, err = s.query(ctx, "SHOW TABLES LIKE ?", record.NewNull())
	require.Error(t, err)
	s.endpoint.query.Catalog = func(context.Context, string) ([]schema.Table, error) {
		tables := make([]schema.Table, MaxCatalogTables+1)
		return tables, nil
	}
	_, err = s.query(ctx, "SHOW TABLES")
	require.Error(t, err)
	require.True(t, discoveryLike("日本語", "日_語"))
	require.False(t, discoveryLike("Users", "users"))
}

func TestDiscoveryRejectsUnsupportedSyntax(t *testing.T) {
	s, _ := discoverySession(t)
	for _, query := range []string{
		"SHOW TABLES FROM missing", "SHOW COLUMNS FROM missing", "SHOW FULL DATABASES", "SHOW FULL INDEX FROM items",
		"SHOW TABLES WHERE 1=1", "SHOW TABLES; SELECT 1", "DESC", "SHOW COLUMNS", "SHOW COLUMNS FROM",
		"SHOW INDEX FROM godb.items FROM godb", "SHOW INDEX FROM items LIKE '%'", "DESC items; garbage",
		"SHOW TABLES LIKE 'a\\%'", "SHOW TABLES LIKE '" + strings.Repeat("x", 65) + "'", "SHOW COLUMNS FROM ``",
	} {
		_, err := s.query(context.Background(), query)
		require.Error(t, err, query)
	}

	for _, query := range []string{"SHOW VARIABLES", "SET NAMES utf8mb4", "SELECT 1", "SELECT * FROM items WHERE id=1"} {
		_, matched, err := parseDiscovery(query)
		require.False(t, matched)
		require.NoError(t, err)
	}
}

func TestDiscoveryResultEncodingLimits(t *testing.T) {
	value, err := record.NewText(strings.Repeat("x", 2048))
	require.NoError(t, err)

	for _, encode := range []func(string, sql.Result) ([][]byte, error){resultPackets, binaryResultPackets} {
		result := sql.Result{Columns: []schema.Column{{Name: "value", Type: record.TypeText}}}
		for range 70 {
			result.Rows = append(result.Rows, []record.Value{value})
		}

		_, err := encode("godb", result)
		require.NoError(t, err)

		for len(result.Rows) < maxResultRows {
			result.Rows = append(result.Rows, []record.Value{value})
		}

		output, err := encode("godb", result)
		require.Error(t, err)
		require.Nil(t, output)
	}
}

func FuzzDiscovery(f *testing.F) {
	for _, query := range []string{"SHOW TABLES", "SHOW COLUMNS FROM godb.items LIKE ?", "DESC items 'e%'", "SHOW INDEX FROM items", "SHOW DATABASES", "SHOW VARIABLES"} {
		f.Add(query)
	}

	f.Fuzz(func(t *testing.T, query string) {
		statement, matched, err := parseDiscovery(query)
		if err != nil || !matched {
			return
		}

		s, _ := discoverySession(t)
		_, _ = s.discovery(context.Background(), statement, true, nil)
		_, _ = s.discovery(context.Background(), statement, false, nil)
	})
}

func BenchmarkDiscoveryIndexes(b *testing.B) {
	s, _ := discoverySession(b)
	for b.Loop() {
		result, err := s.query(context.Background(), "SHOW INDEX FROM items")
		if err != nil {
			b.Fatal(err)
		}

		if len(result.Rows) != 4 {
			b.Fatal(fmt.Sprint(result))
		}
	}
}
