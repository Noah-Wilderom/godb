package schema

import (
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func cloneIndexes(indexes []Index) []Index {
	cloned := slices.Clone(indexes)
	for i := range cloned {
		cloned[i].ColumnIDs = slices.Clone(indexes[i].ColumnIDs)
	}

	return cloned
}

func TestValidateIndexes_Valid(t *testing.T) {
	cases := []struct {
		name    string
		indexes []Index
	}{
		{"Nil", nil},
		{"Empty", []Index{}},
		{"NonUnique", []Index{{ID: 1, Name: "by_name", ColumnIDs: []uint32{7}}}},
		{"UniqueNullable", []Index{{ID: 1, Name: "by_name", ColumnIDs: []uint32{7}, Unique: true}}},
		{"Composite", []Index{{ID: 1, Name: "by_name_id", ColumnIDs: []uint32{7, 42}}}},
		{"PrimaryColumn", []Index{{ID: 42, Name: "by_id", ColumnIDs: []uint32{42}, Unique: true}}},
		{"MultipleOrders", []Index{
			{ID: 100, Name: "by_name_id", ColumnIDs: []uint32{7, 42}},
			{ID: 1, Name: "by_id_name", ColumnIDs: []uint32{42, 7}},
		}},
		{"CaseSensitiveNames", []Index{
			{ID: 1, Name: "Name", ColumnIDs: []uint32{7}},
			{ID: 2, Name: "name", ColumnIDs: []uint32{7}, Unique: true},
		}},
		{"UnicodeAndMaximumID", []Index{{ID: math.MaxUint32, Name: "名前", ColumnIDs: []uint32{7}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			table := testTable()
			table.Indexes = tc.indexes
			before := cloneIndexes(table.Indexes)
			require.NoError(t, ValidateIndexes(table.Columns, table.Indexes))
			require.NoError(t, ValidateTable(table))
			require.Equal(t, before, table.Indexes)
		})
	}
}

func TestValidateIndexes_Invalid(t *testing.T) {
	cases := []struct {
		name    string
		indexes []Index
		want    string
	}{
		{"ReservedID", []Index{{Name: "by_name", ColumnIDs: []uint32{7}}}, "ID 0 is reserved"},
		{"DuplicateID", []Index{
			{ID: 1, Name: "by_name", ColumnIDs: []uint32{7}},
			{ID: 1, Name: "by_id", ColumnIDs: []uint32{42}},
		}, "duplicate ID 1"},
		{"EmptyName", []Index{{ID: 1, ColumnIDs: []uint32{7}}}, "name must not be empty"},
		{"InvalidUTF8", []Index{{ID: 1, Name: "bad\xff", ColumnIDs: []uint32{7}}}, "name must be valid UTF-8"},
		{"DuplicateName", []Index{
			{ID: 1, Name: "by_name", ColumnIDs: []uint32{7}},
			{ID: 2, Name: "by_name", ColumnIDs: []uint32{42}},
		}, "duplicate name \"by_name\""},
		{"NilColumns", []Index{{ID: 1, Name: "empty"}}, "at least one column ID"},
		{"EmptyColumns", []Index{{ID: 1, Name: "empty", ColumnIDs: []uint32{}}}, "at least one column ID"},
		{"ZeroColumnID", []Index{{ID: 1, Name: "bad", ColumnIDs: []uint32{0}}}, "column ID 0 does not exist"},
		{"MissingColumn", []Index{{ID: 1, Name: "bad", ColumnIDs: []uint32{99}}}, "column ID 99 does not exist"},
		{"PositionInsteadOfID", []Index{{ID: 1, Name: "bad", ColumnIDs: []uint32{1}}}, "column ID 1 does not exist"},
		{"DuplicateColumn", []Index{{ID: 1, Name: "bad", ColumnIDs: []uint32{7, 42, 7}}}, "duplicate column ID 7"},
		{"LaterMissingColumn", []Index{{ID: 1, Name: "bad", ColumnIDs: []uint32{7, 99}}}, "column ID 99 does not exist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			table := testTable()
			table.Indexes = tc.indexes
			beforeIndexes := cloneIndexes(table.Indexes)
			beforeColumns := slices.Clone(table.Columns)
			require.ErrorContains(t, ValidateIndexes(table.Columns, table.Indexes), tc.want)
			require.ErrorContains(t, ValidateTable(table), tc.want)
			require.Equal(t, beforeIndexes, table.Indexes)
			require.Equal(t, beforeColumns, table.Columns)
		})
	}
}

func TestValidateIndexes_ColumnTypes(t *testing.T) {
	cases := []struct {
		typ     record.Type
		allowed bool
	}{
		{record.TypeInt64, true},
		{record.TypeUint64, true},
		{record.TypeText, true},
		{record.TypeBytes, true},
		{record.TypeUUID, true},
		{record.TypeBool, false},
		{record.TypeFloat32, false},
		{record.TypeFloat64, false},
		{record.TypeJSON, false},
		{record.TypeDecimal, false},
		{record.TypeDate, false},
		{record.TypeTime, false},
		{record.TypeDateTime, false},
		{record.TypeTimestamp, false},
		{record.TypeDuration, false},
		{record.TypeArray, false},
		{record.TypeMap, false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("Type%02x", tc.typ), func(t *testing.T) {
			table := testTable()
			table.Columns[0].Type = tc.typ
			require.NoError(t, ValidateTable(table), "unindexed columns retain all supported types")
			table.Indexes = []Index{{ID: 1, Name: "by_name", ColumnIDs: []uint32{7}}}

			err := ValidateTable(table)
			if tc.allowed {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "has unsupported type")
			}
		})
	}
}

func TestValidateIndexes_ColumnPrerequisites(t *testing.T) {
	require.ErrorContains(t, ValidateIndexes(nil, nil), "at least one column")
	table := testTable()
	table.Columns[1].ID = table.Columns[0].ID
	require.ErrorContains(t, ValidateIndexes(table.Columns, nil), "duplicate ID")
}

func TestIndexes_StableIdentityAndScope(t *testing.T) {
	table := testTable()
	table.Indexes = []Index{{ID: 100, Name: "by_name_id", ColumnIDs: []uint32{7, 42}, Unique: true}}
	before := cloneIndexes(table.Indexes)
	table.Columns[0].Name = "display_name"
	table.Columns[1].Name = "user_id"
	slices.Reverse(table.Columns)
	table.Indexes[0].Name = "renamed"
	require.NoError(t, ValidateTable(table))
	require.Equal(t, before[0].ID, table.Indexes[0].ID)
	require.Equal(t, before[0].ColumnIDs, table.Indexes[0].ColumnIDs)
	require.Equal(t, before[0].Unique, table.Indexes[0].Unique)
	table.ID = 2
	require.NoError(t, ValidateTable(table), "index IDs and names are scoped to a table")
}

func TestIndexes_ExistingCodecs(t *testing.T) {
	table := testTable()
	row := Row{7: record.NewNull(), 42: record.NewUint64(1)}
	beforeRow, err := EncodeRow(table, row)
	require.NoError(t, err)
	beforeKey, err := EncodePrimaryKey(table, row[42])
	require.NoError(t, err)
	table.Indexes = []Index{{ID: 1, Name: "by_name", ColumnIDs: []uint32{7}, Unique: true}}
	encoded, err := EncodeRow(table, row)
	require.NoError(t, err)
	require.Equal(t, beforeRow, encoded)
	decoded, err := DecodeRow(table, encoded)
	require.NoError(t, err)
	require.Equal(t, row, decoded)
	key, err := EncodePrimaryKey(table, row[42])
	require.NoError(t, err)
	require.Equal(t, beforeKey, key)

	table.Indexes[0].ColumnIDs = []uint32{99}
	require.Error(t, ValidateRow(table, row))
	encoded, err = EncodeRow(table, row)
	require.Error(t, err)
	require.Nil(t, encoded)
	decoded, err = DecodeRow(table, beforeRow)
	require.Error(t, err)
	require.Nil(t, decoded)
	key, err = EncodePrimaryKey(table, row[42])
	require.Error(t, err)
	require.Nil(t, key)
}
