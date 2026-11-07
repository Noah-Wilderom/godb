package schema

import (
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func testTable() Table {
	return Table{
		ID:   1,
		Name: "users",
		Columns: []Column{
			{ID: 7, Name: "name", Type: record.TypeText, Nullable: true},
			{ID: 42, Name: "id", Type: record.TypeUint64},
		},
		PrimaryKeyColumnID: 42,
	}
}

func TestValidateTable_PrimaryKeyTypes(t *testing.T) {
	cases := []struct {
		typ     record.Type
		allowed bool
	}{
		{record.TypeInt64, true},
		{record.TypeBool, false},
		{record.TypeText, true},
		{record.TypeBytes, true},
		{record.TypeFloat64, false},
		{record.TypeJSON, false},
		{record.TypeDate, false},
		{record.TypeTime, false},
		{record.TypeDateTime, false},
		{record.TypeUUID, true},
		{record.TypeDecimal, false},
		{record.TypeMap, false},
		{record.TypeTimestamp, false},
		{record.TypeUint64, true},
		{record.TypeFloat32, false},
		{record.TypeDuration, false},
		{record.TypeArray, false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("Type%02x", tc.typ), func(t *testing.T) {
			table := testTable()
			table.Columns[1].Type = tc.typ

			err := ValidateTable(table)
			if tc.allowed {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "primary key column \"id\" has unsupported type")
			}

			table = testTable()
			table.Columns[0].Type = tc.typ
			require.NoError(t, ValidateTable(table), "non-key columns retain all supported types")
		})
	}
}

func TestValidateTable_Invalid(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Table)
		want   string
	}{
		{"ZeroID", func(table *Table) { table.ID = 0 }, "table ID must be nonzero"},
		{"EmptyName", func(table *Table) { table.Name = "" }, "table name must not be empty"},
		{"InvalidUTF8", func(table *Table) { table.Name = "bad\xff" }, "table name must be valid UTF-8"},
		{"NilColumns", func(table *Table) { table.Columns = nil }, "at least one column"},
		{"EmptyColumns", func(table *Table) { table.Columns = []Column{} }, "at least one column"},
		{"ZeroColumnID", func(table *Table) { table.Columns[0].ID = 0 }, "ID must be nonzero"},
		{"DuplicateColumnID", func(table *Table) { table.Columns[0].ID = 42 }, "duplicate ID 42"},
		{"DuplicateColumnName", func(table *Table) { table.Columns[0].Name = "id" }, "duplicate name"},
		{"InvalidColumnName", func(table *Table) { table.Columns[0].Name = "" }, "name must not be empty"},
		{"InvalidColumnType", func(table *Table) { table.Columns[0].Type = record.Type(0xff) }, "unsupported type 0xff"},
		{"NullPrimaryKeyType", func(table *Table) { table.Columns[1].Type = record.TypeNull }, "TypeNull is not a column type"},
		{"MissingPrimaryKey", func(table *Table) { table.PrimaryKeyColumnID = 0 }, "primary key column ID must be nonzero"},
		{"UnknownPrimaryKey", func(table *Table) { table.PrimaryKeyColumnID = 99 }, "primary key column ID 99 does not exist"},
		{"IndexInsteadOfID", func(table *Table) { table.PrimaryKeyColumnID = 1 }, "primary key column ID 1 does not exist"},
		{"NullablePrimaryKey", func(table *Table) { table.Columns[1].Nullable = true }, "primary key column \"id\" must not be nullable"},
		{"InvalidColumnAfterKey", func(table *Table) {
			table.Columns = append(table.Columns, Column{ID: 100, Name: "id", Type: record.TypeText})
		}, "duplicate name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			table := testTable()
			tc.change(&table)
			before := table
			before.Columns = slices.Clone(table.Columns)
			require.ErrorContains(t, ValidateTable(table), tc.want)
			require.Equal(t, before, table)
		})
	}
}

func TestValidateTable_StableIDsAndNoMutation(t *testing.T) {
	table := testTable()
	table.ID = math.MaxUint32
	table.Name = "利用者"
	table.Columns[1].ID = math.MaxUint32
	table.PrimaryKeyColumnID = math.MaxUint32
	before := table
	before.Columns = slices.Clone(table.Columns)
	require.NoError(t, ValidateTable(table))
	require.Equal(t, before, table)

	table.Name = "renamed_users"
	table.Columns[1].Name = "user_id"
	slices.Reverse(table.Columns)
	require.NoError(t, ValidateTable(table))
	require.Equal(t, before.ID, table.ID)
	require.Equal(t, before.PrimaryKeyColumnID, table.PrimaryKeyColumnID)
}

func TestValidateTable_SingleColumn(t *testing.T) {
	table := testTable()
	table.Columns = table.Columns[1:]
	require.NoError(t, ValidateTable(table))
}
