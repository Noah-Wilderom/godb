package schema

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func TestEncodeIndexPrefix_ComponentsAndNulls(t *testing.T) {
	for _, unique := range []bool{false, true} {
		table, row := indexEntryFixture(t, unique)
		table.Indexes[0].ColumnIDs = []uint32{7, 100}

		for _, null := range []bool{false, true} {
			if null {
				row[7] = record.NewNull()
			}

			entry, err := EncodeIndexEntry(table, 3, row)
			require.NoError(t, err)

			for _, values := range [][]record.Value{nil, {row[7]}, {row[7], row[100]}} {
				prefix, err := EncodeIndexPrefix(table, 3, values...)
				require.NoError(t, err)
				require.True(t, bytes.HasPrefix(entry.Key, prefix))
			}
		}
	}
}

func TestEncodeIndexPrefix_ExactValuesAndValidation(t *testing.T) {
	table, row := indexEntryFixture(t, false)
	prefix, err := EncodeIndexPrefix(table, 3, row[7])
	require.NoError(t, err)
	row[7], err = record.NewText("ab")
	require.NoError(t, err)
	entry, err := EncodeIndexEntry(table, 3, row)
	require.NoError(t, err)
	require.False(t, bytes.HasPrefix(entry.Key, prefix), "a complete string value is not a string-prefix query")

	for _, values := range [][]record.Value{{row[7], row[100]}, {row[100]}} {
		key, err := EncodeIndexPrefix(table, 3, values...)
		require.Error(t, err)
		require.Nil(t, key)
	}

	table.Columns[0].Nullable = false
	key, err := EncodeIndexPrefix(table, 3, record.NewNull())
	require.Error(t, err)
	require.Nil(t, key)

	for _, id := range []uint32{0, 999} {
		key, err := EncodeIndexPrefix(table, id)
		require.Error(t, err)
		require.Nil(t, key)
	}
}
