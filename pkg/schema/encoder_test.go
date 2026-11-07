package schema

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"maps"
	"math"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func rowCodecFixture(t testing.TB) (Table, Row) {
	t.Helper()
	must := func(value record.Value, err error) record.Value {
		t.Helper()
		require.NoError(t, err)
		return value
	}

	values := []record.Value{
		record.NewNull(), record.NewInt64(math.MinInt64), record.NewUint64(math.MaxUint64),
		record.NewBool(false), must(record.NewText("Noah 🌳")), record.NewBytes([]byte{0, 1, 0xff}),
		record.NewFloat64(-1.5), record.NewFloat32(1.5), must(record.NewJSON([]byte(`{"b":2,"a":null}`))),
		must(record.NewDate("2024-02-29")), must(record.NewTime("12:34:56.123")),
		must(record.NewDateTime("2024-02-29 12:34:56.123")),
		must(record.NewUUID("00112233-4455-6677-8899-aabbccddeeff")), must(record.NewDecimal("-123.4500")),
		record.NewTimestamp(-1), record.NewDuration(-60),
		record.NewMap(map[string]record.Value{"z": record.NewNull(), "a": record.NewArray([]record.Value{record.NewInt64(1)})}),
		record.NewArray([]record.Value{record.NewNull(), record.NewBytes([]byte{1, 2, 3})}),
	}
	table := Table{ID: 23, Name: "all_types", PrimaryKeyColumnID: 42}
	table.Columns = []Column{{ID: 42, Name: "id", Type: record.TypeUint64}}
	row := Row{42: record.NewUint64(7)}
	id := uint32(100)

	for _, value := range values {
		column := Column{ID: id, Name: fmt.Sprintf("value_%d", id), Type: value.Type()}
		if value.IsNull() {
			column.Type = record.TypeText
			column.Nullable = true
		}

		table.Columns = append(table.Columns, column)
		row[id] = value
		id++
	}

	return table, row
}

func TestRowCodec_Golden(t *testing.T) {
	table := testTable()
	row := Row{7: record.NewNull(), 42: record.NewUint64(42)}
	want, err := hex.DecodeString("0101000000020000000700000001000000002a000000090000000e2a00000000000000")
	require.NoError(t, err)
	encoded, err := EncodeRow(table, row)
	require.NoError(t, err)
	require.Equal(t, want, encoded)
	decoded, err := DecodeRow(table, want)
	require.NoError(t, err)
	require.Equal(t, row, decoded)
}

func TestRowCodec_AllTypesAndDeterminism(t *testing.T) {
	table, row := rowCodecFixture(t)
	beforeRow := maps.Clone(row)
	beforeTable := table
	beforeTable.Columns = slices.Clone(table.Columns)
	encoded, err := EncodeRow(table, row)
	require.NoError(t, err)
	require.Equal(t, beforeRow, row)
	require.Equal(t, beforeTable, table)
	decoded, err := DecodeRow(table, encoded)
	require.NoError(t, err)
	require.Equal(t, row, decoded)
	again, err := EncodeRow(table, decoded)
	require.NoError(t, err)
	require.Equal(t, encoded, again)

	slices.Reverse(table.Columns)
	table.Name = "renamed"
	reordered := make(Row, len(row))

	for i := range table.Columns {
		column := &table.Columns[i]
		column.Name = fmt.Sprintf("renamed_%d", column.ID)
		reordered[column.ID] = row[column.ID]
	}

	again, err = EncodeRow(table, reordered)
	require.NoError(t, err)
	require.Equal(t, encoded, again)
	decoded, err = DecodeRow(table, encoded)
	require.NoError(t, err)
	require.Equal(t, row, decoded)
}

func TestDecodeRow_EveryTruncatedPrefix(t *testing.T) {
	table, row := rowCodecFixture(t)
	encoded, err := EncodeRow(table, row)
	require.NoError(t, err)

	for end := range len(encoded) {
		decoded, err := DecodeRow(table, encoded[:end])
		require.Error(t, err, "prefix length %d", end)
		require.Nil(t, decoded)
	}
}

func TestDecodeRow_Malformed(t *testing.T) {
	table := testTable()
	valid, err := EncodeRow(table, Row{7: record.NewNull(), 42: record.NewUint64(42)})
	require.NoError(t, err)

	cases := []struct {
		name   string
		change func([]byte) []byte
		want   string
	}{
		{"Version", func(b []byte) []byte { b[0] = 2; return b }, "unsupported row format version"},
		{"TableID", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[1:5], 99); return b }, "does not match table ID"},
		{"ZeroCount", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[5:9], 0); return b }, "column count"},
		{"HugeCount", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[5:9], math.MaxUint32); return b }, "column count"},
		{"CountExceedsBytes", func(b []byte) []byte { return b[:25] }, "exceeds remaining bytes"},
		{"ZeroID", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[9:13], 0); return b }, "nonzero and strictly increasing"},
		{"DuplicateID", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[18:22], 7); return b }, "strictly increasing"},
		{"UnsortedID", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[9:13], 43); return b }, "strictly increasing"},
		{"UnknownID", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[18:22], 99); return b }, "missing column"},
		{"ZeroLength", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[13:17], 0); return b }, "invalid value length"},
		{"HugeLength", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[13:17], math.MaxUint32); return b }, "invalid value length"},
		{"ShortValue", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[22:26], 8); return b }, "truncated 64-bit payload"},
		{"UnknownTag", func(b []byte) []byte { b[17] = 0xff; return b }, "unsupported value type"},
		{"WrongType", func(b []byte) []byte { b[26] = byte(record.TypeInt64); return b }, "has value type"},
		{"NullPrimaryKey", func(b []byte) []byte {
			binary.LittleEndian.PutUint32(b[22:26], 1)
			b[26] = byte(record.TypeNull)
			return b[:27]
		}, "must not be null"},
		{"TrailingValueBytes", func(b []byte) []byte {
			binary.LittleEndian.PutUint32(b[13:17], 2)
			return slices.Insert(b, 18, byte(0))
		}, "trailing bytes after value"},
		{"TrailingRowBytes", func(b []byte) []byte { return append(b, 0) }, "trailing bytes after row"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded := tc.change(bytes.Clone(valid))
			before := bytes.Clone(encoded)
			row, err := DecodeRow(table, encoded)
			require.ErrorContains(t, err, tc.want)
			require.Nil(t, row)
			require.Equal(t, before, encoded)
		})
	}
}

func TestRowCodec_InvalidSchemaAndRow(t *testing.T) {
	table := testTable()
	encoded, err := EncodeRow(table, Row{7: record.NewNull(), 42: record.NewUint64(42)})
	require.NoError(t, err)
	table.Columns[1].Nullable = true
	decoded, err := DecodeRow(table, encoded)
	require.Error(t, err)
	require.Nil(t, decoded)
	result, err := EncodeRow(table, Row{7: record.NewNull(), 42: record.NewUint64(42)})
	require.Error(t, err)
	require.Nil(t, result)

	for _, row := range []Row{nil, {42: record.NewUint64(42)}, {7: record.NewNull(), 42: record.NewInt64(42)}} {
		result, err := EncodeRow(testTable(), row)
		require.Error(t, err)
		require.Nil(t, result)
	}

	table = testTable()
	table.Columns[0].Type = record.TypeArray
	value := record.NewNull()

	for range 66 {
		value = record.NewArray([]record.Value{value})
	}

	result, err = EncodeRow(table, Row{7: value, 42: record.NewUint64(42)})
	require.ErrorContains(t, err, "nesting exceeds maximum depth")
	require.Nil(t, result)
}

func TestDecodeRow_OwnsValues(t *testing.T) {
	table, row := rowCodecFixture(t)
	encoded, err := EncodeRow(table, row)
	require.NoError(t, err)
	before := bytes.Clone(encoded)
	decoded, err := DecodeRow(table, encoded)
	require.NoError(t, err)
	require.Equal(t, before, encoded)
	clear(encoded)
	again, err := EncodeRow(table, decoded)
	require.NoError(t, err)
	require.Equal(t, before, again)
}

func TestCheckEncodedRowSize(t *testing.T) {
	limit := min(uint64(math.MaxUint32), uint64(math.MaxInt))
	require.NoError(t, checkEncodedRowSize(0))
	require.NoError(t, checkEncodedRowSize(limit))
	require.Error(t, checkEncodedRowSize(limit+1))
	require.Error(t, checkEncodedRowSize(math.MaxUint64))
}

func FuzzDecodeRow(f *testing.F) {
	table, row := rowCodecFixture(f)
	encoded, err := EncodeRow(table, row)
	require.NoError(f, err)
	f.Add(encoded)
	f.Add([]byte{})
	f.Add(encoded[:rowHeaderSize])
	f.Add(append(bytes.Clone(encoded), 0))
	f.Fuzz(func(t *testing.T, data []byte) {
		row, err := DecodeRow(table, data)
		if err != nil {
			require.Nil(t, row)
			return
		}

		again, err := EncodeRow(table, row)
		require.NoError(t, err)
		require.Equal(t, data, again)
	})
}
