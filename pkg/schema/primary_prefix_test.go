package schema

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func TestEncodePrimaryPrefix(t *testing.T) {
	table := testTable()
	prefix, err := EncodePrimaryPrefix(table)
	require.NoError(t, err)
	require.Equal(t, []byte{1, 0, 0, 0, 1, 0, 0, 0, 0}, prefix)

	for _, id := range []uint64{0, 1, 255, 1 << 63, ^uint64(0)} {
		key, err := EncodePrimaryKey(table, record.NewUint64(id))
		require.NoError(t, err)
		require.True(t, bytes.HasPrefix(key, prefix))
	}

	table.ID = 2
	other, err := EncodePrimaryPrefix(table)
	require.NoError(t, err)
	require.False(t, bytes.HasPrefix(other, prefix))
	table.ID = 0
	_, err = EncodePrimaryPrefix(table)
	require.Error(t, err)
}
