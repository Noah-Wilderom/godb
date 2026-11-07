package schema

import (
	"bytes"
	"encoding/hex"
	"math"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func TestEncodePrimaryKey_GoldenAndIdentity(t *testing.T) {
	table := testTable()
	value := record.NewUint64(256)
	want, err := hex.DecodeString("0100000001000000000e0000000000000100")
	require.NoError(t, err)
	key, err := EncodePrimaryKey(table, value)
	require.NoError(t, err)
	require.Equal(t, want, key)

	table.Name = "renamed"
	table.Columns[1].Name = "user_id"
	slices.Reverse(table.Columns)
	again, err := EncodePrimaryKey(table, value)
	require.NoError(t, err)
	require.Equal(t, key, again)

	var previous []byte

	for _, id := range []uint32{1, 255, 256, math.MaxUint32} {
		table.ID = id
		encoded, err := EncodePrimaryKey(table, value)
		require.NoError(t, err)
		require.Equal(t, []byte{0, 0, 0, 0}, encoded[5:9])

		if previous != nil {
			require.Negative(t, bytes.Compare(previous, encoded))
		}

		previous = encoded
	}
}

func TestEncodePrimaryKey_SupportedTypes(t *testing.T) {
	text, err := record.NewText("a\x00b")
	require.NoError(t, err)
	id, err := record.NewUUID("00112233-4455-6677-8899-aabbccddeeff")
	require.NoError(t, err)

	for _, value := range []record.Value{record.NewInt64(-1), record.NewUint64(42), text, record.NewBytes([]byte{0, 0xff}), id} {
		table := testTable()
		table.Columns[1].Type = value.Type()
		before := table
		before.Columns = slices.Clone(table.Columns)
		key, err := EncodePrimaryKey(table, value)
		require.NoError(t, err)
		component, err := record.EncodeKeyValue(value)
		require.NoError(t, err)
		require.Equal(t, component, key[9:])
		require.Equal(t, before, table)
	}
}

func TestEncodePrimaryKey_RejectsInvalidInput(t *testing.T) {
	for _, value := range []record.Value{record.NewNull(), record.NewInt64(1), record.NewBool(false)} {
		key, err := EncodePrimaryKey(testTable(), value)
		require.Error(t, err)
		require.Nil(t, key)
	}

	key, err := EncodePrimaryKey(Table{}, record.NewUint64(1))
	require.ErrorContains(t, err, "table ID must be nonzero")
	require.Nil(t, key)
	table := testTable()
	table.PrimaryKeyColumnID = 99
	key, err = EncodePrimaryKey(table, record.NewUint64(1))
	require.ErrorContains(t, err, "does not exist")
	require.Nil(t, key)
	table = testTable()
	table.Columns[1].Nullable = true
	key, err = EncodePrimaryKey(table, record.NewUint64(1))
	require.ErrorContains(t, err, "must not be nullable")
	require.Nil(t, key)
}
