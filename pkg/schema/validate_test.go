package schema

import (
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func TestValidateColumns_SupportedTypes(t *testing.T) {
	types := []record.Type{
		record.TypeInt64, record.TypeBool, record.TypeText, record.TypeBytes,
		record.TypeFloat64, record.TypeJSON, record.TypeDate, record.TypeTime,
		record.TypeDateTime, record.TypeUUID, record.TypeDecimal, record.TypeMap,
		record.TypeTimestamp, record.TypeUint64, record.TypeFloat32,
		record.TypeDuration, record.TypeArray,
	}
	for _, typ := range types {
		for _, nullable := range []bool{false, true} {
			t.Run(fmt.Sprintf("Type%02x/Nullable%t", typ, nullable), func(t *testing.T) {
				require.NoError(t, ValidateColumns([]Column{
					{ID: 1, Name: "value", Type: typ, Nullable: nullable},
				}))
			})
		}
	}
}

func TestValidateColumns_Invalid(t *testing.T) {
	valid := Column{ID: 1, Name: "id", Type: record.TypeUint64}

	cases := []struct {
		name    string
		columns []Column
		want    string
	}{
		{"Nil", nil, "at least one column"},
		{"Empty", []Column{}, "at least one column"},
		{"ZeroID", []Column{{Name: "id", Type: record.TypeUint64}}, "ID must be nonzero"},
		{"DuplicateID", []Column{valid, {ID: 1, Name: "name", Type: record.TypeText}}, "duplicate ID 1"},
		{"EmptyName", []Column{{ID: 1, Type: record.TypeText}}, "name must not be empty"},
		{"InvalidUTF8", []Column{{ID: 1, Name: "bad\xff", Type: record.TypeText}}, "name must be valid UTF-8"},
		{"DuplicateName", []Column{valid, {ID: 2, Name: "id", Type: record.TypeText}}, "duplicate name \"id\""},
		{"NullType", []Column{{ID: 1, Name: "value", Type: record.TypeNull}}, "TypeNull is not a column type"},
		{"NullableNullType", []Column{{ID: 1, Name: "value", Type: record.TypeNull, Nullable: true}}, "TypeNull is not a column type"},
		{"UnknownType", []Column{{ID: 1, Name: "value", Type: record.Type(0x12)}}, "unsupported type 0x12"},
		{"MaximumType", []Column{{ID: 1, Name: "value", Type: record.Type(0xff)}}, "unsupported type 0xff"},
		{"LaterInvalidColumn", []Column{valid, {ID: 2, Name: "value", Type: record.TypeNull}}, "column 1 (\"value\")"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := slices.Clone(tc.columns)
			require.ErrorContains(t, ValidateColumns(tc.columns), tc.want)
			require.Equal(t, before, tc.columns)
		})
	}
}

func TestValidateColumns_PreservesNamesIDsAndOrder(t *testing.T) {
	columns := []Column{
		{ID: math.MaxUint32, Name: "Name", Type: record.TypeText},
		{ID: 7, Name: "name", Type: record.TypeText, Nullable: true},
		{ID: 100, Name: "名前", Type: record.TypeText},
		{ID: 1, Name: " name ", Type: record.TypeText},
	}
	before := slices.Clone(columns)
	require.NoError(t, ValidateColumns(columns))
	require.Equal(t, before, columns)
	columns[0].Name = "renamed"
	require.NoError(t, ValidateColumns(columns))
	require.Equal(t, before[0].ID, columns[0].ID)
}
