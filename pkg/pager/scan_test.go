package pager

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBPlusInternal_SeparatorAt(t *testing.T) {
	node, err := NewBPlusInternal([][]byte{nil, []byte("b")}, []uint64{1, 2, 3})
	require.NoError(t, err)
	empty, found := node.SeparatorAt(0)
	require.True(t, found)
	require.NotNil(t, empty)
	require.Empty(t, empty)
	key, found := node.SeparatorAt(1)
	require.True(t, found)
	require.Equal(t, []byte("b"), key)
	clear(key)
	key, found = node.SeparatorAt(1)
	require.True(t, found)
	require.Equal(t, []byte("b"), key)

	for _, index := range []int{-1, 2, 100} {
		key, found := node.SeparatorAt(index)
		require.False(t, found)
		require.Nil(t, key)
	}
}

func TestBPlusLeaf_ScanAccess(t *testing.T) {
	leaf, err := NewBPlusLeaf(byteStrings("", "b", "d"), byteStrings("zero", "one", "two"))
	require.NoError(t, err)

	for _, tc := range []struct {
		key   string
		index int
	}{{"", 0}, {"a", 1}, {"b", 1}, {"c", 2}, {"d", 2}, {"z", 3}} {
		require.Equal(t, tc.index, leaf.LowerBound([]byte(tc.key)))
	}

	key, value, found := leaf.EntryAt(1)
	require.True(t, found)
	require.Equal(t, []byte("b"), key)
	require.Equal(t, []byte("one"), value)
	clear(key)
	clear(value)
	key, value, found = leaf.EntryAt(1)
	require.True(t, found)
	require.Equal(t, []byte("b"), key)
	require.Equal(t, []byte("one"), value)

	for _, index := range []int{-1, 3, 100} {
		key, value, found := leaf.EntryAt(index)
		require.False(t, found)
		require.Nil(t, key)
		require.Nil(t, value)
	}

	empty, err := NewBPlusLeaf(nil, nil)
	require.NoError(t, err)
	require.Zero(t, empty.LowerBound(nil))
}

func TestBPlusInternal_ChildrenForRange(t *testing.T) {
	node, err := NewBPlusInternal(byteStrings("b", "d", "f"), []uint64{1, 2, 3, 4})
	require.NoError(t, err)

	cases := []struct {
		start, end []byte
		want       []uint64
	}{
		{nil, nil, []uint64{1, 2, 3, 4}},
		{nil, []byte("b"), []uint64{1}},
		{[]byte("b"), []byte("d"), []uint64{2}},
		{[]byte("c"), []byte("e"), []uint64{2, 3}},
		{[]byte("f"), nil, []uint64{4}},
		{[]byte("z"), nil, []uint64{4}},
		{nil, []byte{}, nil},
		{[]byte("d"), []byte("d"), nil},
		{[]byte("f"), []byte("a"), nil},
	}
	for _, tc := range cases {
		got := node.ChildrenForRange(tc.start, tc.end)
		require.Equal(t, tc.want, got)
		clear(got)
		require.Equal(t, tc.want, node.ChildrenForRange(tc.start, tc.end))
	}

	single, err := NewBPlusInternal(nil, []uint64{1})
	require.NoError(t, err)
	require.Equal(t, []uint64{1}, single.ChildrenForRange(nil, nil))
}
