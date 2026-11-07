package schema

import (
	"bytes"
	"cmp"
	"encoding/hex"
	"maps"
	"math"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func indexEntryFixture(t testing.TB, unique bool) (Table, Row) {
	t.Helper()
	name, err := record.NewText("a")
	require.NoError(t, err)
	table := testTable()
	table.Columns = append(table.Columns, Column{ID: 100, Name: "group", Type: record.TypeInt64, Nullable: true})
	table.Indexes = []Index{{ID: 3, Name: "by_name", ColumnIDs: []uint32{7}, Unique: unique}}
	return table, Row{7: name, 42: record.NewUint64(1), 100: record.NewInt64(-2)}
}

func TestEncodeIndexEntry_Golden(t *testing.T) {
	cases := []struct {
		name      string
		unique    bool
		null      bool
		composite bool
		key       string
	}{
		{"NonUnique", false, false, false, "010000000100000003036100000e0000000000000001"},
		{"Unique", true, false, false, "01000000010000000303610000"},
		{"NullNonUnique", false, true, false, "010000000100000003000e0000000000000001"},
		{"NullUnique", true, true, false, "010000000100000003000e0000000000000001"},
		{"Composite", false, false, true, "01000000010000000303610000017ffffffffffffffe0e0000000000000001"},
		{"UniqueComposite", true, false, true, "01000000010000000303610000017ffffffffffffffe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			table, row := indexEntryFixture(t, tc.unique)

			if tc.null {
				row[7] = record.NewNull()
			}

			if tc.composite {
				table.Indexes[0].ColumnIDs = []uint32{7, 100}
			}

			entry, err := EncodeIndexEntry(table, 3, row)
			require.NoError(t, err)
			key, err := hex.DecodeString(tc.key)
			require.NoError(t, err)
			value, err := hex.DecodeString("0100000001000000000e0000000000000001")
			require.NoError(t, err)
			require.Equal(t, key, entry.Key)
			require.Equal(t, value, entry.Value)
			require.Equal(t, tc.unique && !tc.null, entry.RequiresUniqueCheck)
		})
	}
}

func TestEncodeIndexEntry_DuplicateValues(t *testing.T) {
	for _, unique := range []bool{false, true} {
		for _, null := range []bool{false, true} {
			table, first := indexEntryFixture(t, unique)

			if null {
				first[7] = record.NewNull()
			}

			second := maps.Clone(first)
			second[42] = record.NewUint64(2)
			a, err := EncodeIndexEntry(table, 3, first)
			require.NoError(t, err)
			b, err := EncodeIndexEntry(table, 3, second)
			require.NoError(t, err)
			require.Equal(t, unique && !null, bytes.Equal(a.Key, b.Key))
			require.NotEqual(t, a.Value, b.Value)

			if !a.RequiresUniqueCheck {
				require.Negative(t, bytes.Compare(a.Key, b.Key))
			}
		}
	}
}

func TestEncodeIndexEntry_CompositeNulls(t *testing.T) {
	for _, nullIDs := range [][]uint32{{7}, {100}, {7, 100}} {
		table, row := indexEntryFixture(t, true)
		table.Indexes[0].ColumnIDs = []uint32{7, 100}

		for _, id := range nullIDs {
			row[id] = record.NewNull()
		}

		first, err := EncodeIndexEntry(table, 3, row)
		require.NoError(t, err)
		require.False(t, first.RequiresUniqueCheck)
		row[42] = record.NewUint64(2)
		second, err := EncodeIndexEntry(table, 3, row)
		require.NoError(t, err)
		require.Negative(t, bytes.Compare(first.Key, second.Key))
	}
}

func TestEncodeIndexEntry_CompositeOrdering(t *testing.T) {
	table, row := indexEntryFixture(t, false)
	table.Indexes[0].ColumnIDs = []uint32{7, 100}

	var previous []byte

	for _, text := range []string{"", "\x00", "a", "a\x00", "aa", "🌳"} {
		value, err := record.NewText(text)
		require.NoError(t, err)
		row[7] = value

		for _, group := range []record.Value{record.NewNull(), record.NewInt64(math.MinInt64), record.NewInt64(-1), record.NewInt64(0), record.NewInt64(math.MaxInt64)} {
			row[100] = group
			entry, err := EncodeIndexEntry(table, 3, row)
			require.NoError(t, err)

			if previous != nil {
				require.Negative(t, bytes.Compare(previous, entry.Key))
			}

			previous = entry.Key
		}
	}
}

func TestEncodeIndexEntry_PrimaryKeyTypes(t *testing.T) {
	text, err := record.NewText("pk\x00")
	require.NoError(t, err)
	id, err := record.NewUUID("00112233-4455-6677-8899-aabbccddeeff")
	require.NoError(t, err)

	for _, value := range []record.Value{record.NewInt64(-42), record.NewUint64(42), text, record.NewBytes([]byte{0, 0xff}), id} {
		table, row := indexEntryFixture(t, false)
		table.Columns[1].Type = value.Type()
		row[42] = value
		entry, err := EncodeIndexEntry(table, 3, row)
		require.NoError(t, err)
		primary, err := EncodePrimaryKey(table, value)
		require.NoError(t, err)
		require.Equal(t, primary, entry.Value)
		component, err := record.EncodeKeyValue(value)
		require.NoError(t, err)
		require.True(t, bytes.HasSuffix(entry.Key, component))
	}
}

func TestEncodeIndexEntry_RejectsInvalidInput(t *testing.T) {
	table, row := indexEntryFixture(t, false)

	for _, id := range []uint32{0, 99} {
		entry, err := EncodeIndexEntry(table, id, row)
		require.ErrorContains(t, err, "secondary index ID")
		require.Equal(t, IndexEntry{}, entry)
	}

	for _, change := range []func(Table, Row){
		func(_ Table, row Row) { delete(row, 7) },
		func(_ Table, row Row) { delete(row, 100) },
		func(_ Table, row Row) { row[7] = record.NewInt64(1) },
		func(_ Table, row Row) { row[42] = record.NewNull() },
		func(_ Table, row Row) { row[99] = record.NewNull() },
		func(table Table, row Row) { table.Columns[0].Nullable = false; row[7] = record.NewNull() },
		func(table Table, _ Row) { table.Indexes[0].ColumnIDs = []uint32{99} },
	} {
		table, row := indexEntryFixture(t, false)
		change(table, row)
		entry, err := EncodeIndexEntry(table, 3, row)
		require.Error(t, err)
		require.Equal(t, IndexEntry{}, entry)
	}
}

func TestEncodeIndexEntry_IdentityAndOwnership(t *testing.T) {
	table, row := indexEntryFixture(t, false)
	table.Indexes[0].ColumnIDs = []uint32{7, 100}
	beforeRow := maps.Clone(row)
	beforeTable := table
	beforeTable.Columns = slices.Clone(table.Columns)
	beforeTable.Indexes = cloneIndexes(table.Indexes)
	entry, err := EncodeIndexEntry(table, 3, row)
	require.NoError(t, err)
	require.Equal(t, beforeRow, row)
	require.Equal(t, beforeTable, table)
	table.Name = "renamed"
	table.Indexes[0].Name = "renamed_index"
	table.Columns[0].Name = "display_name"
	slices.Reverse(table.Columns)
	again, err := EncodeIndexEntry(table, 3, maps.Clone(row))
	require.NoError(t, err)
	require.Equal(t, entry, again)

	slices.Reverse(table.Indexes[0].ColumnIDs)
	reordered, err := EncodeIndexEntry(table, 3, row)
	require.NoError(t, err)
	require.NotEqual(t, entry.Key, reordered.Key)
	table.Indexes = append(table.Indexes, Index{ID: 4, Name: "other", ColumnIDs: []uint32{7, 100}})
	other, err := EncodeIndexEntry(table, 4, row)
	require.NoError(t, err)
	require.NotEqual(t, entry.Key, other.Key)
	require.Equal(t, entry.Value, other.Value)
	table.ID = 2
	otherTable, err := EncodeIndexEntry(table, 4, row)
	require.NoError(t, err)
	require.NotEqual(t, other.Key, otherTable.Key)
	require.NotEqual(t, other.Value, otherTable.Value)

	clear(entry.Key)
	require.Equal(t, again.Value, entry.Value)
	clear(entry.Value)
	fresh, err := EncodeIndexEntry(beforeTable, 3, row)
	require.NoError(t, err)
	require.Equal(t, again, fresh)
}

func TestCheckIndexKeySize(t *testing.T) {
	limit := min(uint64(math.MaxUint32), uint64(math.MaxInt))
	require.NoError(t, checkIndexKeySize(limit))
	require.Error(t, checkIndexKeySize(limit+1))
	require.Error(t, checkIndexKeySize(math.MaxUint64))
}

func TestEncodeIndexEntries_OrderAndOwnership(t *testing.T) {
	table, row := indexEntryFixture(t, false)
	table.Indexes = append(table.Indexes,
		Index{ID: 1, Name: "by_group", ColumnIDs: []uint32{100}, Unique: true},
		Index{ID: 7, Name: "by_both", ColumnIDs: []uint32{7, 100}},
	)
	before := cloneIndexes(table.Indexes)
	entries, err := EncodeIndexEntries(table, row)
	require.NoError(t, err)
	require.Equal(t, before, table.Indexes)
	require.Len(t, entries, len(table.Indexes))

	for i, index := range table.Indexes {
		want, err := EncodeIndexEntry(table, index.ID, row)
		require.NoError(t, err)
		require.Equal(t, want, entries[i])
		clear(entries[i].Key)
		require.Equal(t, want.Value, entries[i].Value)
		clear(entries[i].Value)
	}

	row[7] = record.NewNull()
	entries, err = EncodeIndexEntries(table, row)
	require.NoError(t, err)
	require.False(t, entries[0].RequiresUniqueCheck)
	require.True(t, entries[1].RequiresUniqueCheck)
	require.False(t, entries[2].RequiresUniqueCheck)
}

func TestEncodeIndexEntries_EmptyAndInvalid(t *testing.T) {
	table, row := indexEntryFixture(t, false)
	table.Indexes = nil
	entries, err := EncodeIndexEntries(table, row)
	require.NoError(t, err)
	require.Nil(t, entries)
	delete(row, 7)
	entries, err = EncodeIndexEntries(table, row)
	require.Error(t, err)
	require.Nil(t, entries)
	table, row = indexEntryFixture(t, false)
	table.Indexes = append(table.Indexes, Index{ID: 99, Name: "bad", ColumnIDs: []uint32{999}})
	entries, err = EncodeIndexEntries(table, row)
	require.Error(t, err)
	require.Nil(t, entries)
}

func FuzzEncodeIndexEntry_Order(f *testing.F) {
	f.Add([]byte("a"), []byte("aa"), uint64(1), uint64(2), false, false, false)
	f.Add([]byte{}, []byte{0}, uint64(2), uint64(1), true, false, true)
	f.Add([]byte{0, 0xff}, []byte{0, 0xff}, uint64(1), uint64(2), false, false, true)
	f.Fuzz(func(t *testing.T, a, b []byte, x, y uint64, nullA, nullB, unique bool) {
		table, first := indexEntryFixture(t, unique)
		table.Columns[0].Type = record.TypeBytes
		second := maps.Clone(first)
		first[7], second[7] = record.NewBytes(a), record.NewBytes(b)
		first[42], second[42] = record.NewUint64(x), record.NewUint64(y)

		if nullA {
			first[7] = record.NewNull()
		}

		if nullB {
			second[7] = record.NewNull()
		}

		left, err := EncodeIndexEntry(table, 3, first)
		require.NoError(t, err)
		right, err := EncodeIndexEntry(table, 3, second)
		require.NoError(t, err)
		order := bytes.Compare(a, b)

		switch {
		case nullA && nullB:
			order = 0
		case nullA:
			order = -1
		case nullB:
			order = 1
		}

		if order == 0 && (!unique || nullA) {
			order = cmp.Compare(x, y)
		}

		require.Equal(t, order, bytes.Compare(left.Key, right.Key))
		require.Equal(t, unique && !nullA, left.RequiresUniqueCheck)
		require.Equal(t, unique && !nullB, right.RequiresUniqueCheck)
	})
}
