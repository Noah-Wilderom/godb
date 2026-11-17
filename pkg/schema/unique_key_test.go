package schema

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func TestEncodeUniqueIndexKey_MatchesStoredEntry(t *testing.T) {
	table, row := indexEntryFixture(t, true)

	for _, ids := range [][]uint32{{7}, {100, 7}, {7, 100}} {
		table.Indexes[0].ColumnIDs = ids
		values := make([]record.Value, 0, len(ids))

		for _, id := range ids {
			values = append(values, row[id])
		}

		entry, err := EncodeIndexEntry(table, 3, row)
		require.NoError(t, err)
		key, err := EncodeUniqueIndexKey(table, 3, values...)
		require.NoError(t, err)
		require.Equal(t, entry.Key, key)
		slices.Reverse(table.Columns)
		table.Indexes[0].Name = "renamed"
		again, err := EncodeUniqueIndexKey(table, 3, values...)
		require.NoError(t, err)
		require.Equal(t, key, again)
		clear(key)
		require.Equal(t, entry.Key, again)
	}
}

func TestEncodeUniqueIndexKey_RejectsInvalidLookups(t *testing.T) {
	table, row := indexEntryFixture(t, true)
	table.Indexes[0].ColumnIDs = []uint32{7, 100}

	for _, values := range [][]record.Value{
		nil,
		{row[7]},
		{row[7], row[100], row[42]},
		{row[100], row[7]},
		{record.NewNull(), row[100]},
		{row[7], record.NewNull()},
	} {
		key, err := EncodeUniqueIndexKey(table, 3, values...)
		require.Error(t, err)
		require.Nil(t, key)
	}

	for _, id := range []uint32{0, 99} {
		key, err := EncodeUniqueIndexKey(table, id, row[7], row[100])
		require.ErrorContains(t, err, "does not exist")
		require.Nil(t, key)
	}

	table.Indexes[0].Unique = false
	key, err := EncodeUniqueIndexKey(table, 3, row[7], row[100])
	require.ErrorContains(t, err, "is not unique")
	require.Nil(t, key)
	key, err = EncodeUniqueIndexKey(Table{}, 3, row[7])
	require.Error(t, err)
	require.Nil(t, key)
}

func TestEncodeUniqueIndexKey_SupportedTypes(t *testing.T) {
	text, err := record.NewText("a\x00b")
	require.NoError(t, err)
	id, err := record.NewUUID("00112233-4455-6677-8899-aabbccddeeff")
	require.NoError(t, err)

	for _, value := range []record.Value{record.NewInt64(-42), record.NewUint64(42), text, record.NewBytes([]byte{0, 0xff}), id} {
		table, row := indexEntryFixture(t, true)
		table.Columns[0].Type = value.Type()
		row[7] = value
		entry, err := EncodeIndexEntry(table, 3, row)
		require.NoError(t, err)
		key, err := EncodeUniqueIndexKey(table, 3, value)
		require.NoError(t, err)
		require.Equal(t, entry.Key, key)
	}
}
