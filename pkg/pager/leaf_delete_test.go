package pager

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBPlusLeaf_Delete(t *testing.T) {
	for _, order := range [][]string{{"", "b", "d"}, {"d", "b", ""}, {"b", "", "d"}} {
		leaf, err := NewBPlusLeaf(byteStrings("", "b", "d"), byteStrings("empty", "bee", "dee"))
		require.NoError(t, err)
		keys, values := leaf.keys, leaf.values
		require.False(t, leaf.Delete([]byte("missing")))

		for i, key := range order {
			require.True(t, leaf.Delete([]byte(key)))
			require.False(t, leaf.Delete([]byte(key)))
			require.NoError(t, ValidateLeaf(leaf))
			require.Len(t, leaf.keys, len(order)-i-1)
			_, _, found := leaf.Get([]byte(key))
			require.False(t, found)
			require.Nil(t, keys[len(order)-i-1])
			require.Nil(t, values[len(order)-i-1])
			encoded, err := EncodeLeaf(leaf)
			require.NoError(t, err)
			decoded, err := DecodeLeaf(encoded)
			require.NoError(t, err)
			require.Equal(t, leaf.keys, decoded.keys)
		}

		leaf.Put([]byte("new"), []byte("value"))
		value, _, found := leaf.Get([]byte("new"))
		require.True(t, found)
		require.Equal(t, []byte("value"), value)
	}
}
