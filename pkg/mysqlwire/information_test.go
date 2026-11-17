package mysqlwire

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func infoText(t testing.TB, text string) record.Value {
	t.Helper()
	value, err := record.NewText(text)
	require.NoError(t, err)
	return value
}

func TestInformationSchemaViews(t *testing.T) {
	s, reads := discoverySession(t)

	cases := []struct {
		query         string
		rows, columns int
	}{
		{"SELECT * FROM information_schema.schemata", 1, 5},
		{"SELECT * FROM INFORMATION_SCHEMA.TABLES", 1, 10},
		{"SELECT * FROM `information_schema`.`columns`", 4, 21},
		{"SELECT * FROM information_schema.statistics", 4, 18},
		{"SELECT * FROM information_schema.table_constraints", 2, 7},
		{"SELECT * FROM information_schema.key_column_usage", 2, 12},
	}
	for _, entry := range cases {
		result, err := s.query(context.Background(), entry.query)
		require.NoError(t, err, entry.query)
		require.Len(t, result.Rows, entry.rows)
		require.Len(t, result.Columns, entry.columns)
		description, err := s.describe(context.Background(), entry.query)
		require.NoError(t, err)
		require.Equal(t, result.Columns, description.Columns)

		for _, row := range result.Rows {
			require.Len(t, row, len(result.Columns))

			for i, value := range row {
				if value.IsNull() {
					require.True(t, result.Columns[i].Nullable)
				} else {
					require.Equal(t, result.Columns[i].Type, value.Type())
				}
			}
		}

		_, err = resultPackets("godb", result)
		require.NoError(t, err)
		_, err = binaryResultPackets("godb", result)
		require.NoError(t, err)
	}

	require.Equal(t, 12, *reads)
	result, err := s.query(context.Background(), "SELECT ENGINE,TABLE_ROWS,AUTO_INCREMENT FROM information_schema.tables")
	require.NoError(t, err)
	require.Equal(t, "GoDB", textCell(t, result.Rows[0][0]))
	require.True(t, result.Rows[0][1].IsNull())
	require.True(t, result.Rows[0][2].IsNull())
	result, err = s.query(context.Background(), "SELECT COLUMN_NAME,DATA_TYPE,COLUMN_TYPE,NUMERIC_PRECISION,COLUMN_KEY FROM information_schema.columns ORDER BY ORDINAL_POSITION")
	require.NoError(t, err)
	require.Equal(t, "bigint", textCell(t, result.Rows[0][1]))
	require.Equal(t, "bigint unsigned", textCell(t, result.Rows[0][2]))
	require.Equal(t, record.NewInt64(20), result.Rows[0][3])
	require.Equal(t, "godb_uuid", textCell(t, result.Rows[3][1]))
	require.Equal(t, "UNI", textCell(t, result.Rows[1][4]))
	result, err = s.query(context.Background(), "SELECT INDEX_NAME,SEQ_IN_INDEX,COLUMN_NAME,NON_UNIQUE,CARDINALITY FROM information_schema.statistics ORDER BY INDEX_NAME,SEQ_IN_INDEX")
	require.NoError(t, err)
	require.Len(t, result.Rows, 4)
	require.Equal(t, "active_email", textCell(t, result.Rows[2][0]))
	require.Equal(t, record.NewInt64(2), result.Rows[2][1])
	require.Equal(t, "email", textCell(t, result.Rows[2][2]))
	require.True(t, result.Rows[2][4].IsNull())
}

func TestInformationFilteringOrderingAndParameters(t *testing.T) {
	s, _ := discoverySession(t)
	ctx := context.Background()
	query := "SELECT c.COLUMN_NAME AS name,c.ORDINAL_POSITION AS position FROM information_schema.columns AS c WHERE c.TABLE_SCHEMA=DATABASE() AND c.TABLE_NAME=? AND c.ORDINAL_POSITION=? ORDER BY position DESC LIMIT 2 OFFSET 0"
	description, err := s.describe(ctx, query)
	require.NoError(t, err)
	require.Len(t, description.Parameters, 2)
	require.Equal(t, record.TypeText, description.Parameters[0].Type)
	require.Equal(t, record.TypeInt64, description.Parameters[1].Type)
	result, err := s.query(ctx, query, infoText(t, "items"), record.NewUint64(2))
	require.NoError(t, err)
	require.Equal(t, description.Columns, result.Columns)
	require.Len(t, result.Rows, 1)
	require.Equal(t, "email", textCell(t, result.Rows[0][0]))

	for _, args := range [][]record.Value{{infoText(t, "items")}, {infoText(t, "items"), infoText(t, "2")}, {infoText(t, "items"), record.NewUint64(math.MaxUint64)}} {
		_, err := s.query(ctx, query, args...)
		require.Error(t, err)
	}

	result, err = s.query(ctx, query, infoText(t, "items"), record.NewNull())
	require.NoError(t, err)
	require.Empty(t, result.Rows)
	result, err = s.query(ctx, "SELECT COLUMN_NAME FROM information_schema.columns WHERE COLUMN_NAME LIKE ? ORDER BY ORDINAL_POSITION DESC LIMIT 1 OFFSET 1", infoText(t, "e%"))
	require.NoError(t, err)
	require.Equal(t, "email", textCell(t, result.Rows[0][0]))

	for _, entry := range []struct {
		where string
		count int
	}{
		{"TABLE_SCHEMA='missing'", 0}, {"TABLE_NAME='Items'", 0}, {"COLUMN_DEFAULT IS NULL", 4}, {"COLUMN_DEFAULT IS NOT NULL", 0}, {"COLUMN_DEFAULT=NULL", 0}, {"ORDINAL_POSITION=1", 1},
	} {
		result, err := s.query(ctx, "SELECT COUNT(*) AS total FROM information_schema.columns WHERE "+entry.where)
		require.NoError(t, err)
		require.Equal(t, "total", result.Columns[0].Name)
		require.Equal(t, record.NewInt64(int64(entry.count)), result.Rows[0][0])
	}

	result, err = s.query(ctx, "SELECT COUNT(*) FROM information_schema.tables LIMIT 0")
	require.NoError(t, err)
	require.Empty(t, result.Rows)
	result, err = s.query(ctx, "SELECT TABLE_NAME FROM information_schema.tables WHERE TABLE_NAME=?", infoText(t, "items' OR 1=1 --"))
	require.NoError(t, err)
	require.Empty(t, result.Rows)
}

func TestInformationRejectsUnsupportedOrAmbiguousSQL(t *testing.T) {
	s, _ := discoverySession(t)
	for _, query := range []string{
		"SELECT * FROM information_schema.unknown",
		"SELECT missing FROM information_schema.columns",
		"SELECT c.COLUMN_NAME FROM information_schema.columns d",
		"SELECT COUNT(COLUMN_NAME) FROM information_schema.columns",
		"SELECT COUNT(*),COLUMN_NAME FROM information_schema.columns",
		"SELECT * FROM information_schema.columns WHERE ORDINAL_POSITION LIKE '1'",
		"SELECT * FROM information_schema.columns WHERE ORDINAL_POSITION='1'",
		"SELECT * FROM information_schema.columns WHERE COLUMN_NAME='id' OR COLUMN_NAME='email'",
		"SELECT * FROM information_schema.columns WHERE ORDINAL_POSITION>1",
		"SELECT * FROM information_schema.columns WHERE TABLE_NAME IN ('items')",
		"SELECT * FROM information_schema.columns JOIN information_schema.tables ON 1=1",
		"SELECT * FROM information_schema.columns LIMIT ?",
		"SELECT * FROM information_schema.columns LIMIT 4097",
		"SELECT * FROM information_schema.columns; SELECT 1",
		"SELECT * FROM information_schema.columns WHERE TABLE_SCHEMA=VERSION()",
		"SELECT COLUMN_NAME AS x,TABLE_NAME AS x FROM information_schema.columns ORDER BY x",
		"SELECT * FROM information_schema.columns ORDER BY missing",
		"SELECT * FROM information_schema.columns GROUP BY TABLE_NAME",
		"SELECT * FROM information_schema.columns WHERE COLUMN_NAME LIKE '" + strings.Repeat("a", 65) + "'",
		"SELECT * FROM information_schema.columns WHERE ORDINAL_POSITION=18446744073709551615",
		"SELECT * FROM information_schema.columns WHERE TABLE_NAME='items' garbage",
	} {
		_, err := s.query(context.Background(), query)
		require.Error(t, err, query)
		_, err = s.describe(context.Background(), query)
		require.Error(t, err, query)
	}

	for _, query := range []string{"SELECT * FROM items WHERE id=1", "SELECT name FROM items WHERE name='information_schema'", "SELECT * FROM information_schema WHERE id=-1", "SELECT VERSION()"} {
		_, matched, _ := parseInformation(query)
		require.False(t, matched, query)
	}
}

func TestInformationBoundsAndCancellation(t *testing.T) {
	s, _ := discoverySession(t)
	table := discoveryFixture()
	s.endpoint.query.Catalog = func(context.Context, string) ([]schema.Table, error) {
		tables := make([]schema.Table, MaxCatalogTables)
		for i := range tables {
			tables[i] = table
		}

		return tables, nil
	}
	query := "SELECT COUNT(*) FROM information_schema.columns"
	result, err := s.query(context.Background(), query)
	require.NoError(t, err)
	require.Equal(t, record.NewInt64(4096), result.Rows[0][0])
	table.Columns = append(table.Columns, schema.Column{ID: 5, Name: "fifth", Type: record.TypeText})
	_, err = s.query(context.Background(), "SELECT COLUMN_NAME FROM information_schema.columns LIMIT 1")
	require.Error(t, err)
	result, err = s.query(context.Background(), query)
	require.NoError(t, err)
	require.Equal(t, record.NewInt64(5120), result.Rows[0][0])
	result, err = s.query(context.Background(), "SELECT COLUMN_NAME FROM information_schema.columns WHERE COLUMN_NAME='id'")
	require.NoError(t, err)
	require.Len(t, result.Rows, 1024)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.query(ctx, query)
	require.Error(t, err)

	for len(table.Columns) < 65 {
		table.Columns = append(table.Columns, schema.Column{ID: uint32(len(table.Columns) + 1), Name: "extra", Type: record.TypeText})
	}

	_, err = s.query(context.Background(), query)
	require.Error(t, err)
}

func TestInformationNullOrdering(t *testing.T) {
	s, _ := discoverySession(t)
	for _, direction := range []string{"ASC", "DESC"} {
		result, err := s.query(context.Background(), "SELECT COLUMN_NAME,NUMERIC_PRECISION FROM information_schema.columns ORDER BY NUMERIC_PRECISION "+direction+",ORDINAL_POSITION")
		require.NoError(t, err)

		if direction == "ASC" {
			require.True(t, result.Rows[0][1].IsNull())
		} else {
			require.Equal(t, record.NewInt64(20), result.Rows[0][1])
		}
	}
}

func FuzzInformationSchema(f *testing.F) {
	for _, query := range []string{"SELECT * FROM information_schema.table_constraints WHERE CONSTRAINT_TYPE='UNIQUE'", "SELECT COLUMN_NAME FROM information_schema.key_column_usage WHERE CONSTRAINT_NAME=? ORDER BY ORDINAL_POSITION", "SELECT * FROM information_schema.columns", "SELECT COUNT(*) AS n FROM information_schema.tables WHERE TABLE_SCHEMA=DATABASE()", "SELECT c.COLUMN_NAME FROM information_schema.columns c WHERE TABLE_NAME=? ORDER BY ORDINAL_POSITION DESC LIMIT 10"} {
		f.Add(query)
	}

	f.Fuzz(func(t *testing.T, query string) {
		statement, matched, err := parseInformation(query)
		if err != nil || !matched {
			return
		}

		s, _ := discoverySession(t)
		_, _, _ = s.information(context.Background(), statement, true, nil)
		_, _, _ = s.information(context.Background(), statement, false, nil)
	})
}

func BenchmarkInformationSchema(b *testing.B) {
	s, _ := discoverySession(b)
	for b.Loop() {
		_, err := s.query(context.Background(), "SELECT COLUMN_NAME,DATA_TYPE FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='items' ORDER BY ORDINAL_POSITION")
		if err != nil {
			b.Fatal(err)
		}
	}
}
