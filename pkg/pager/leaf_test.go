package pager

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBPlusLeaf_Get(t *testing.T) {
	keys := byteStrings("", "10", "2", "cat", "catalog", "猫", "\xff")
	values := byteStrings("empty key", "ten", "two", "", "longer value", "猫咪", "\x00\xff")
	leaf, err := NewBPlusLeaf(keys, values)
	require.NoError(t, err)

	for i, key := range keys {
		value, index, found := leaf.Get(key)
		require.True(t, found, "key %q", key)
		assert.Equal(t, i, index)
		assert.True(t, bytes.Equal(values[i], value))
	}

	for _, key := range byteStrings("1", "11", "3", "ca", "cats", "\xff\xff") {
		value, _, found := leaf.Get(key)
		assert.False(t, found, "key %q", key)
		assert.Nil(t, value)
	}
}

func TestBPlusLeaf_GetOnEmptyLeaf(t *testing.T) {
	leaf, err := NewBPlusLeaf(nil, nil)
	require.NoError(t, err)
	value, _, found := leaf.Get([]byte("missing"))
	assert.False(t, found)
	assert.Nil(t, value)
}

func TestBPlusLeaf_FirstKey(t *testing.T) {
	cases := []struct {
		name  string
		keys  [][]byte
		want  []byte
		found bool
	}{
		{"EmptyLeaf", nil, nil, false},
		{"NilKey", [][]byte{nil}, nil, true},
		{"EmptyKey", [][]byte{{}}, []byte{}, true},
		{"FirstOfSeveral", byteStrings("cat", "dog"), []byte("cat"), true},
		{"BinaryKey", byteStrings("\x00\xff", "cat"), []byte{0, 0xff}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaf, err := NewBPlusLeaf(tc.keys, make([][]byte, len(tc.keys)))
			require.NoError(t, err)
			key, found := leaf.FirstKey()
			assert.Equal(t, tc.found, found)
			assert.True(t, bytes.Equal(tc.want, key))

			if !found {
				assert.Nil(t, key)
			}
		})
	}
}

func TestBPlusLeaf_FirstKeyReturnsIndependentCopy(t *testing.T) {
	leaf, err := NewBPlusLeaf(byteStrings("cat", "dog"), byteStrings("one", "two"))
	require.NoError(t, err)
	first, found := leaf.FirstKey()
	require.True(t, found)
	second, found := leaf.FirstKey()
	require.True(t, found)
	first[0] = 'z'
	assert.Equal(t, []byte("cat"), second)
	value, _, found := leaf.Get([]byte("cat"))
	require.True(t, found)
	assert.Equal(t, []byte("one"), value)
	leaf.Put([]byte("ant"), []byte("new first"))
	assert.Equal(t, []byte("cat"), second)
	key, found := leaf.FirstKey()
	require.True(t, found)
	assert.Equal(t, []byte("ant"), key)
}

func TestBPlusLeaf_PutMaintainsByteOrderAndReplacesValues(t *testing.T) {
	leaf, err := NewBPlusLeaf(nil, nil)
	require.NoError(t, err)

	for _, key := range byteStrings("cat", "2", "catalog", "10", "", "\x00", "\xff") {
		leaf.Put(key, append([]byte("value:"), key...))
	}

	leaf.Put([]byte("cat"), []byte("replacement with a different length"))
	expected := byteStrings("", "\x00", "10", "2", "cat", "catalog", "\xff")
	assert.Equal(t, expected, leaf.keys)

	for i, key := range expected {
		value, index, found := leaf.Get(key)
		require.True(t, found)
		assert.Equal(t, i, index)

		want := append([]byte("value:"), key...)
		if string(key) == "cat" {
			want = []byte("replacement with a different length")
		}

		assert.Equal(t, want, value)
	}
}

func TestBPlusLeaf_NilAndEmptyKeysAreEquivalent(t *testing.T) {
	leaf, err := NewBPlusLeaf(nil, nil)
	require.NoError(t, err)
	leaf.Put(nil, []byte("first"))
	leaf.Put([]byte{}, nil)
	require.Len(t, leaf.keys, 1)

	for _, key := range [][]byte{nil, {}} {
		value, _, found := leaf.Get(key)
		assert.True(t, found)
		assert.Empty(t, value)
	}
}

func TestNewBPlusLeaf_RejectsInvalidEntries(t *testing.T) {
	cases := []struct {
		name         string
		keys, values [][]byte
	}{
		{"MoreKeys", byteStrings("a", "b"), byteStrings("value")},
		{"MoreValues", byteStrings("a"), byteStrings("one", "two")},
		{"Descending", byteStrings("b", "a"), byteStrings("", "")},
		{"Duplicate", byteStrings("a", "a"), byteStrings("", "")},
		{"LastKeyOutOfOrder", byteStrings("a", "c", "b"), byteStrings("", "", "")},
		{"NilAndEmptyDuplicate", [][]byte{nil, {}}, byteStrings("", "")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaf, err := NewBPlusLeaf(tc.keys, tc.values)
			require.Error(t, err)
			assert.Nil(t, leaf)
		})
	}
}

func TestNewBPlusLeaf_CopiesOuterAndInnerSlices(t *testing.T) {
	keys, values := byteStrings("cat", "dog"), byteStrings("animal", "pet")
	leaf, err := NewBPlusLeaf(keys, values)
	require.NoError(t, err)
	keys[0][0] = 'z'
	values[0][0] = 'X'
	keys[1] = []byte("other")
	values[1] = nil
	assert.Equal(t, byteStrings("cat", "dog"), leaf.keys)
	assert.Equal(t, byteStrings("animal", "pet"), leaf.values)
}

func TestBPlusLeaf_PutCopiesInsertedAndReplacedBytes(t *testing.T) {
	leaf, err := NewBPlusLeaf(nil, nil)
	require.NoError(t, err)
	key, value := []byte("cat"), []byte("animal")
	leaf.Put(key, value)
	key[0], value[0] = 'z', 'X'
	assert.Equal(t, byteStrings("cat"), leaf.keys)
	assert.Equal(t, byteStrings("animal"), leaf.values)
	replacement := []byte("new value")
	leaf.Put([]byte("cat"), replacement)
	replacement[0] = 'X'
	assert.Equal(t, byteStrings("new value"), leaf.values)
}

func TestBPlusLeaf_GetReturnsIndependentValue(t *testing.T) {
	leaf, err := NewBPlusLeaf(byteStrings("cat"), byteStrings("animal"))
	require.NoError(t, err)
	value, _, found := leaf.Get([]byte("cat"))
	require.True(t, found)
	value[0] = 'X'
	again, _, found := leaf.Get([]byte("cat"))
	require.True(t, found)
	assert.Equal(t, []byte("animal"), again)
}

func TestBPlusLeaf_OverflowIsAllowedInMemoryButCannotBeEncoded(t *testing.T) {
	leaf, err := NewBPlusLeaf(byteStrings("a"), [][]byte{bytes.Repeat([]byte{'v'}, 4088)})
	require.NoError(t, err)
	_, err = EncodeLeaf(leaf)
	require.NoError(t, err)
	leaf.Put([]byte("b"), nil)
	page, err := EncodeLeaf(leaf)
	require.ErrorIs(t, err, ErrPagerPageFull)
	assert.Nil(t, page)
	_, err = NewBPlusLeaf(leaf.keys, leaf.values)
	require.NoError(t, err)
}

func byteStrings(values ...string) [][]byte {
	result := make([][]byte, len(values))
	for i, value := range values {
		result[i] = []byte(value)
	}

	return result
}

func fixtureLeaf(count, valueSize int) *BPlusLeaf {
	leaf := &BPlusLeaf{keys: make([][]byte, count), values: make([][]byte, count)}
	for i := range count {
		leaf.keys[i] = []byte(fmt.Sprintf("%04d", i))
		leaf.values[i] = bytes.Repeat([]byte{byte(i)}, valueSize)
	}

	return leaf
}

func TestBPlusLeaf_SplitPreservesEntriesAndFitsEveryPage(t *testing.T) {
	cases := []struct {
		name        string
		valueSizes  []int
		groupCounts []int
	}{
		{"Empty", nil, []int{0}},
		{"SingleEntry", []int{10}, []int{1}},
		{"AlreadyFits", []int{0, 2, 100}, []int{3}},
		{"SingleExactFit", []int{4088}, []int{1}},
		{"CombinedExactFit", []int{2000, 2083}, []int{2}},
		{"OneByteOverCapacity", []int{2000, 2084}, []int{1, 1}},
		{"ExactFitThenAnotherEntry", []int{4088, 0}, []int{1, 1}},
		{"ResetSpaceForNewGroup", []int{3000, 2000, 2000}, []int{1, 2}},
		{"ThreePagesRequired", []int{1995, 2995, 1995}, []int{1, 1, 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keys := make([][]byte, len(tc.valueSizes))

			values := make([][]byte, len(tc.valueSizes))
			for i, size := range tc.valueSizes {
				keys[i] = []byte{byte('a' + i)}
				values[i] = bytes.Repeat([]byte{byte(i + 1)}, size)
			}

			original, err := NewBPlusLeaf(keys, values)
			require.NoError(t, err)

			leaves, err := original.Split()

			require.NoError(t, err)
			require.Len(t, leaves, len(tc.groupCounts))

			var allKeys, allValues [][]byte

			for i, leaf := range leaves {
				require.NotNil(t, leaf)
				require.NotSame(t, original, leaf)
				require.Len(t, leaf.keys, tc.groupCounts[i])
				require.Len(t, leaf.values, tc.groupCounts[i])
				require.NoError(t, ValidateLeaf(leaf))
				page, err := EncodeLeaf(leaf)
				require.NoError(t, err)
				require.Len(t, page, 4096)
				allKeys = append(allKeys, leaf.keys...)
				allValues = append(allValues, leaf.values...)
			}

			require.Len(t, allKeys, len(keys))
			require.Len(t, allValues, len(values))

			for i := range keys {
				assert.Equal(t, keys[i], allKeys[i])
				assert.Equal(t, values[i], allValues[i])
			}

			assert.Equal(t, keys, original.keys)
			assert.Equal(t, values, original.values)
		})
	}
}

func TestBPlusLeaf_SplitHandlesManyEntries(t *testing.T) {
	original := fixtureLeaf(1200, 0)

	leaves, err := original.Split()

	require.NoError(t, err)
	require.Len(t, leaves, 3)
	index := 0

	for i, wantCount := range []int{511, 511, 178} {
		require.Len(t, leaves[i].keys, wantCount)
		require.NoError(t, ValidateLeaf(leaves[i]))

		for j := range leaves[i].keys {
			assert.Equal(t, original.keys[index], leaves[i].keys[j])
			assert.Equal(t, original.values[index], leaves[i].values[j])
			index++
		}
	}

	assert.Equal(t, 1200, index)
}

func TestBPlusLeaf_SplitPreservesEmptyKeyAndValue(t *testing.T) {
	original, err := NewBPlusLeaf([][]byte{nil}, [][]byte{nil})
	require.NoError(t, err)

	leaves, err := original.Split()

	require.NoError(t, err)
	require.Len(t, leaves, 1)
	require.NotSame(t, original, leaves[0])
	require.Len(t, leaves[0].keys, 1)
	value, _, found := leaves[0].Get([]byte{})
	assert.True(t, found)
	assert.Empty(t, value)
}

func TestBPlusLeaf_SplitOutputsOwnTheirData(t *testing.T) {
	for _, valueSize := range []int{10, 2500} {
		t.Run(fmt.Sprintf("ValueBytes%d", valueSize), func(t *testing.T) {
			sharedValue := bytes.Repeat([]byte{'v'}, valueSize)
			original := &BPlusLeaf{keys: byteStrings("a", "b"), values: [][]byte{sharedValue, sharedValue}}
			leaves, err := original.Split()
			require.NoError(t, err)
			first := leaves[0]
			last := leaves[len(leaves)-1]
			lastIndex := len(last.keys) - 1

			first.keys[0][0] = 'x'
			first.values[0][0] = 'X'
			assert.Equal(t, byteStrings("a", "b"), original.keys)
			assert.Equal(t, byte('v'), original.values[0][0])
			assert.Equal(t, byte('v'), last.values[lastIndex][0])

			original.keys[1][0] = 'z'
			sharedValue[1] = 'Y'
			assert.Equal(t, []byte("b"), last.keys[lastIndex])
			assert.Equal(t, byte('v'), first.values[0][1])
			assert.Equal(t, byte('v'), last.values[lastIndex][1])

			first.keys[0] = []byte("replacement")
			first.values[0] = nil
			assert.Equal(t, []byte("a"), original.keys[0])
			assert.Len(t, original.values[0], valueSize)
		})
	}
}

func TestBPlusLeaf_SplitRejectsInvalidInputWithoutPartialResults(t *testing.T) {
	cases := []struct {
		name string
		leaf *BPlusLeaf
		want error
	}{
		{"Nil", nil, nil},
		{"MismatchedLengths", &BPlusLeaf{keys: byteStrings("a")}, nil},
		{"DescendingKeys", &BPlusLeaf{keys: byteStrings("b", "a"), values: byteStrings("", "")}, nil},
		{"DuplicateKeys", &BPlusLeaf{keys: byteStrings("a", "a"), values: byteStrings("", "")}, nil},
		{"OversizedKey", &BPlusLeaf{keys: [][]byte{make([]byte, 4090)}, values: [][]byte{nil}}, ErrPagerEntryTooLarge},
		{"OversizedValue", &BPlusLeaf{keys: [][]byte{nil}, values: [][]byte{make([]byte, 4090)}}, ErrPagerEntryTooLarge},
		{"OversizedCombinedPayload", &BPlusLeaf{keys: byteStrings("cat"), values: [][]byte{make([]byte, 4087)}}, ErrPagerEntryTooLarge},
		{"OversizedEntryAfterFullPage", &BPlusLeaf{keys: byteStrings("a", "b", "c"), values: [][]byte{make([]byte, 2500), make([]byte, 2500), make([]byte, 4090)}}, ErrPagerEntryTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaves, err := tc.leaf.Split()
			require.Error(t, err)

			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			}

			assert.Nil(t, leaves)
		})
	}
}
