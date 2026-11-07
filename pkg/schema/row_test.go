package schema

import (
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func TestValidateRow_Types(t *testing.T) {
	must := func(value record.Value, err error) record.Value {
		t.Helper()
		require.NoError(t, err)
		return value
	}

	values := []record.Value{
		record.NewInt64(-42), record.NewUint64(42), record.NewBool(false),
		must(record.NewText("")), record.NewBytes(nil), record.NewFloat64(1.5), record.NewFloat32(1.5),
		must(record.NewJSON([]byte("null"))), must(record.NewDate("2024-02-29")),
		must(record.NewTime("12:34:56")), must(record.NewDateTime("2024-02-29 12:34:56")),
		must(record.NewUUID("00112233-4455-6677-8899-aabbccddeeff")), must(record.NewDecimal("123.450")),
		record.NewMap(map[string]record.Value{"nested": record.NewNull()}),
		record.NewTimestamp(-1), record.NewDuration(-60), record.NewArray([]record.Value{record.NewNull()}),
	}
	for _, value := range values {
		for _, nullable := range []bool{false, true} {
			t.Run(fmt.Sprintf("Type%02x/Nullable%t", value.Type(), nullable), func(t *testing.T) {
				table := testTable()
				table.Columns[0].Type = value.Type()
				table.Columns[0].Nullable = nullable
				row := Row{7: value, 42: record.NewUint64(0)}
				require.NoError(t, ValidateRow(table, row))

				for _, other := range values {
					if other.Type() == value.Type() {
						continue
					}

					row[7] = other
					require.ErrorContains(t, ValidateRow(table, row), "has value type")
				}

				row[7] = record.NewNull()
				if nullable {
					require.NoError(t, ValidateRow(table, row))
				} else {
					require.ErrorContains(t, ValidateRow(table, row), "must not be null")
				}
			})
		}
	}
}

func TestValidateRow_Invalid(t *testing.T) {
	cases := []struct {
		name string
		row  Row
		want string
	}{
		{"Nil", nil, "missing column \"name\" (ID 7)"},
		{"Empty", Row{}, "missing column \"name\" (ID 7)"},
		{"MissingNullable", Row{42: record.NewUint64(1)}, "missing column \"name\" (ID 7)"},
		{"MissingPrimaryKey", Row{7: record.NewNull()}, "missing column \"id\" (ID 42)"},
		{"NullPrimaryKey", Row{7: record.NewNull(), 42: record.NewNull()}, "column \"id\" (ID 42) must not be null"},
		{"ZeroValuePrimaryKey", Row{7: record.NewNull(), 42: {}}, "must not be null"},
		{"WrongPrimaryKeyType", Row{7: record.NewNull(), 42: record.NewInt64(1)}, "has value type 0x01; expected 0x0e"},
		{"UnknownColumn", Row{7: record.NewNull(), 42: record.NewUint64(1), 99: record.NewNull()}, "1 unknown column IDs"},
		{"ZeroColumnID", Row{7: record.NewNull(), 42: record.NewUint64(1), 0: record.NewNull()}, "1 unknown column IDs"},
		{"MultipleUnknownColumns", Row{7: record.NewNull(), 42: record.NewUint64(1), 99: record.NewNull(), 100: record.NewNull()}, "2 unknown column IDs"},
		{"UnknownReplacesKnown", Row{7: record.NewNull(), 99: record.NewUint64(1)}, "missing column \"id\" (ID 42)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := maps.Clone(tc.row)
			require.ErrorContains(t, ValidateRow(testTable(), tc.row), tc.want)
			require.Equal(t, before, tc.row)
		})
	}
}

func TestValidateRow_InvalidTable(t *testing.T) {
	table := testTable()
	table.Columns[1].Nullable = true
	row := Row{7: record.NewNull(), 42: record.NewNull()}
	require.ErrorContains(t, ValidateRow(table, row), "primary key column \"id\" must not be nullable")

	table = testTable()
	table.Columns = append(table.Columns, table.Columns[0])
	require.ErrorContains(t, ValidateRow(table, row), "duplicate ID 7")
	require.ErrorContains(t, ValidateRow(Table{}, nil), "table ID must be nonzero")
}

func TestValidateRow_StableIDsAndNoMutation(t *testing.T) {
	table := testTable()
	row := Row{7: {}, 42: record.NewUint64(1)}
	beforeRow := maps.Clone(row)
	beforeTable := table
	beforeTable.Columns = slices.Clone(table.Columns)
	require.NoError(t, ValidateRow(table, row))
	require.Equal(t, beforeRow, row)
	require.Equal(t, beforeTable, table)

	table.Columns[0].Name = "display_name"
	table.Columns[1].Name = "user_id"
	slices.Reverse(table.Columns)
	require.NoError(t, ValidateRow(table, row))
	require.Equal(t, beforeRow, row)
}
