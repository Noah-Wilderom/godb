package pager

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBPlusInternal_InsertSplitAtEveryChildPosition(t *testing.T) {
	cases := []struct {
		name          string
		child         uint64
		separators    [][]byte
		rightChildren []uint64
		wantKeys      [][]byte
		wantChildren  []uint64
	}{
		{"First", 8, byteStrings("c"), []uint64{9}, byteStrings("c", "m", "t"), []uint64{8, 9, 2, 7}},
		{"Middle", 2, byteStrings("p"), []uint64{9}, byteStrings("m", "p", "t"), []uint64{8, 2, 9, 7}},
		{"Last", 7, byteStrings("z"), []uint64{9}, byteStrings("m", "t", "z"), []uint64{8, 2, 7, 9}},
		{"FirstMultiple", 8, byteStrings("c", "h"), []uint64{9, 10}, byteStrings("c", "h", "m", "t"), []uint64{8, 9, 10, 2, 7}},
		{"MiddleMultiple", 2, byteStrings("p", "r"), []uint64{9, 10}, byteStrings("m", "p", "r", "t"), []uint64{8, 2, 9, 10, 7}},
		{"LastMultiple", 7, byteStrings("u", "z"), []uint64{9, 10}, byteStrings("m", "t", "u", "z"), []uint64{8, 2, 7, 9, 10}},
		{"EmptyAndBinaryKeys", 8, byteStrings("", "\x00\xff"), []uint64{9, 10}, byteStrings("", "\x00\xff", "m", "t"), []uint64{8, 9, 10, 2, 7}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node, err := NewBPlusInternal(byteStrings("m", "t"), []uint64{8, 2, 7})
			require.NoError(t, err)
			require.NoError(t, node.InsertSplit(tc.child, tc.separators, tc.rightChildren))
			assert.Equal(t, tc.wantKeys, node.keys)
			assert.Equal(t, tc.wantChildren, node.children)
			require.NoError(t, ValidateInternal(node))
			for i, key := range tc.wantKeys {
				assert.Equal(t, tc.wantChildren[i+1], node.ChildFor(key), "key %x", key)
			}
			page, err := EncodeInternal(node)
			require.NoError(t, err)
			decoded, err := DecodeInternal(page)
			require.NoError(t, err)
			assert.Equal(t, tc.wantKeys, decoded.keys)
			assert.Equal(t, tc.wantChildren, decoded.children)
		})
	}
}

func TestBPlusInternal_InsertSplitIntoSingleChildNode(t *testing.T) {
	node, err := NewBPlusInternal(nil, []uint64{5})
	require.NoError(t, err)
	require.NoError(t, node.InsertSplit(5, byteStrings("cat", "dog"), []uint64{6, 7}))
	assert.Equal(t, byteStrings("cat", "dog"), node.keys)
	assert.Equal(t, []uint64{5, 6, 7}, node.children)
	assert.Equal(t, uint64(5), node.ChildFor([]byte("ant")))
	assert.Equal(t, uint64(6), node.ChildFor([]byte("cat")))
	assert.Equal(t, uint64(7), node.ChildFor([]byte("dog")))
}

func TestBPlusInternal_InsertSplitRejectsInvalidInputWithoutMutation(t *testing.T) {
	cases := []struct {
		name       string
		child      uint64
		separators [][]byte
		children   []uint64
		want       error
	}{
		{"NoSeparators", 2, nil, nil, ErrPagerInvalidArgument},
		{"TooFewChildren", 2, byteStrings("p", "r"), []uint64{9}, ErrPagerInvalidArgument},
		{"TooManyChildren", 2, byteStrings("p"), []uint64{9, 10}, ErrPagerInvalidArgument},
		{"UnknownChild", 99, byteStrings("p"), []uint64{9}, ErrPagerInvalidArgument},
		{"ChildZero", 0, byteStrings("p"), []uint64{9}, ErrPagerInvalidArgument},
		{"ChildTooLarge", maxPageID + 1, byteStrings("p"), []uint64{9}, ErrPagerInvalidArgument},
		{"DescendingSeparators", 2, byteStrings("r", "p"), []uint64{9, 10}, ErrPagerInvalidArgument},
		{"DuplicateSeparators", 2, byteStrings("p", "p"), []uint64{9, 10}, ErrPagerInvalidArgument},
		{"EqualLowerBoundary", 2, byteStrings("m"), []uint64{9}, ErrPagerInvalidArgument},
		{"EqualUpperBoundary", 2, byteStrings("t"), []uint64{9}, ErrPagerInvalidArgument},
		{"BelowLowerBoundary", 2, byteStrings("a"), []uint64{9}, ErrPagerInvalidArgument},
		{"AboveUpperBoundary", 2, byteStrings("z"), []uint64{9}, ErrPagerInvalidArgument},
		{"WrongFirstChildRange", 8, byteStrings("z"), []uint64{9}, ErrPagerInvalidArgument},
		{"WrongLastChildRange", 7, byteStrings("a"), []uint64{9}, ErrPagerInvalidArgument},
		{"NewChildZero", 2, byteStrings("p"), []uint64{0}, ErrPagerInvalidInternalNode},
		{"NewChildTooLarge", 2, byteStrings("p"), []uint64{maxPageID + 1}, ErrPagerInvalidInternalNode},
		{"ReusesSplitChild", 2, byteStrings("p"), []uint64{2}, ErrPagerInvalidInternalNode},
		{"ReusesOtherChild", 2, byteStrings("p"), []uint64{7}, ErrPagerInvalidInternalNode},
		{"DuplicateNewChildren", 2, byteStrings("p", "r"), []uint64{9, 9}, ErrPagerInvalidInternalNode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keyBacking := byteStrings("m", "t", "keep1", "keep2", "keep3", "keep4")
			childBacking := []uint64{8, 2, 7, 90, 91, 92, 93}
			node := &BPlusInternal{keys: keyBacking[:2], children: childBacking[:3]}
			before, err := EncodeInternal(node)
			require.NoError(t, err)
			require.ErrorIs(t, node.InsertSplit(tc.child, tc.separators, tc.children), tc.want)
			assert.Equal(t, byteStrings("m", "t"), node.keys)
			assert.Equal(t, []uint64{8, 2, 7}, node.children)
			assert.Equal(t, byteStrings("m", "t", "keep1", "keep2", "keep3", "keep4"), keyBacking)
			assert.Equal(t, []uint64{8, 2, 7, 90, 91, 92, 93}, childBacking)
			after, err := EncodeInternal(node)
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

func TestBPlusInternal_InsertSplitOwnsInputAndPreservesBackingArrays(t *testing.T) {
	oldKeys := byteStrings("m", "t", "keep1", "keep2")
	oldChildren := []uint64{8, 2, 7, 90, 91, 92}
	node := &BPlusInternal{keys: oldKeys[:2], children: oldChildren[:3]}
	separatorBacking := byteStrings("p", "r", "keep3", "keep4")
	childBacking := []uint64{9, 10, 93, 94}
	require.NoError(t, node.InsertSplit(2, separatorBacking[:2], childBacking[:2]))
	assert.Equal(t, byteStrings("m", "t", "keep1", "keep2"), oldKeys)
	assert.Equal(t, []uint64{8, 2, 7, 90, 91, 92}, oldChildren)
	assert.Equal(t, byteStrings("p", "r", "keep3", "keep4"), separatorBacking)
	assert.Equal(t, []uint64{9, 10, 93, 94}, childBacking)
	separatorBacking[0][0] = 'z'
	separatorBacking[1] = nil
	childBacking[0] = 99
	oldKeys[0][0] = 'a'
	oldChildren[0] = 100
	assert.Equal(t, byteStrings("m", "p", "r", "t"), node.keys)
	assert.Equal(t, []uint64{8, 2, 9, 10, 7}, node.children)
}

func TestBPlusInternal_InsertSplitAllowsTemporaryPageOverflow(t *testing.T) {
	first := bytes.Repeat([]byte{'a'}, 2000)
	second := bytes.Repeat([]byte{'b'}, 2100)
	node, err := NewBPlusInternal([][]byte{first}, []uint64{1, 2})
	require.NoError(t, err)
	require.NoError(t, ValidateInternal(node))
	require.NoError(t, node.InsertSplit(2, [][]byte{second}, []uint64{3}))
	assert.Equal(t, [][]byte{first, second}, node.keys)
	assert.Equal(t, []uint64{1, 2, 3}, node.children)
	page, err := EncodeInternal(node)
	require.ErrorIs(t, err, ErrPagerPageFull)
	assert.Nil(t, page)
}

func TestNewBPlusInternal_AcceptsValidNodes(t *testing.T) {
	cases := []struct {
		name     string
		keys     [][]byte
		children []uint64
	}{
		{"SingleChild", nil, []uint64{1}},
		{"OneSeparator", byteStrings("cat"), []uint64{1, 2}},
		{"ByteOrdering", byteStrings("", "\x00", "10", "2", "cat", "catalog", "猫", "\xff"), []uint64{9, 8, 7, 6, 5, 4, 3, 2, 1}},
		{"MaximumPageID", byteStrings("cat"), []uint64{1, maxPageID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node, err := NewBPlusInternal(tc.keys, tc.children)
			require.NoError(t, err)
			require.NotNil(t, node)
			assert.Len(t, node.keys, len(tc.keys))
			for i := range tc.keys {
				assert.Equal(t, tc.keys[i], node.keys[i])
			}
			assert.Equal(t, tc.children, node.children)
		})
	}
}

func TestNewBPlusInternal_RejectsInvalidNodes(t *testing.T) {
	cases := []struct {
		name     string
		keys     [][]byte
		children []uint64
		wantErr  error
	}{
		{"NoChildren", nil, nil, ErrPagerInvalidInternalNode},
		{"TooFewChildren", byteStrings("cat", "dog"), []uint64{1, 2}, ErrPagerInvalidInternalNode},
		{"TooManyChildren", byteStrings("cat"), []uint64{1, 2, 3}, ErrPagerInvalidInternalNode},
		{"Descending", byteStrings("dog", "cat"), []uint64{1, 2, 3}, ErrPagerInvalidArgument},
		{"DuplicateKeys", byteStrings("cat", "cat"), []uint64{1, 2, 3}, ErrPagerInvalidArgument},
		{"LastKeyOutOfOrder", byteStrings("a", "c", "b"), []uint64{1, 2, 3, 4}, ErrPagerInvalidArgument},
		{"NilAndEmptyDuplicate", [][]byte{nil, {}}, []uint64{1, 2, 3}, ErrPagerInvalidArgument},
		{"PageZero", nil, []uint64{0}, ErrPagerInvalidInternalNode},
		{"LastChildPageZero", byteStrings("cat"), []uint64{1, 0}, ErrPagerInvalidInternalNode},
		{"PageIDTooLarge", byteStrings("cat"), []uint64{1, maxPageID + 1}, ErrPagerInvalidInternalNode},
		{"MaximumUint64", nil, []uint64{^uint64(0)}, ErrPagerInvalidInternalNode},
		{"AdjacentDuplicateChildren", byteStrings("cat"), []uint64{1, 1}, ErrPagerInvalidInternalNode},
		{"SeparatedDuplicateChildren", byteStrings("cat", "dog"), []uint64{1, 2, 1}, ErrPagerInvalidInternalNode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node, err := NewBPlusInternal(tc.keys, tc.children)
			assert.ErrorIs(t, err, tc.wantErr)
			assert.Nil(t, node)
		})
	}
}

func TestNewBPlusInternal_CopiesInputs(t *testing.T) {
	keys := byteStrings("cat", "dog")
	children := []uint64{1, 2, 3}
	node, err := NewBPlusInternal(keys, children)
	require.NoError(t, err)
	keys[0][0] = 'z'
	keys[1] = []byte("other")
	children[0] = 99
	assert.Equal(t, byteStrings("cat", "dog"), node.keys)
	assert.Equal(t, []uint64{1, 2, 3}, node.children)
	assert.Equal(t, uint64(2), node.ChildFor([]byte("cat")))

	node.keys[0][1] = 'u'
	node.keys[1] = nil
	node.children[1] = 88
	assert.Equal(t, byteStrings("zat", "other"), keys)
	assert.Equal(t, []uint64{99, 2, 3}, children)
}

func TestBPlusInternal_ChildFor(t *testing.T) {
	node, err := NewBPlusInternal(byteStrings("cat", "dog"), []uint64{7, 3, 9})
	require.NoError(t, err)
	cases := []struct {
		key  string
		want uint64
	}{
		{"", 7}, {"ant", 7}, {"ca", 7}, {"cat", 3},
		{"catalog", 3}, {"cow", 3}, {"dog", 9}, {"zebra", 9},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("Key_%q", tc.key), func(t *testing.T) {
			assert.Equal(t, tc.want, node.ChildFor([]byte(tc.key)))
		})
	}
	assert.Equal(t, uint64(7), node.ChildFor(nil))
}

func TestBPlusInternal_ChildForSingleChild(t *testing.T) {
	node, err := NewBPlusInternal(nil, []uint64{42})
	require.NoError(t, err)
	for _, key := range [][]byte{nil, {}, []byte("cat"), {0xff}} {
		assert.Equal(t, uint64(42), node.ChildFor(key))
	}
}

func TestBPlusInternal_ChildForByteOrdering(t *testing.T) {
	node, err := NewBPlusInternal(byteStrings("", "\x00", "10", "2", "cat", "catalog", "猫", "\xff"), []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9})
	require.NoError(t, err)
	cases := []struct {
		key  []byte
		want uint64
	}{
		{nil, 2}, {[]byte{}, 2}, {[]byte{0}, 3}, {[]byte{0, 0}, 3},
		{[]byte("10"), 4}, {[]byte("11"), 4}, {[]byte("2"), 5},
		{[]byte("cat"), 6}, {[]byte("cata"), 6}, {[]byte("catalog"), 7},
		{[]byte("猫"), 8}, {[]byte{0xfe}, 8}, {[]byte{0xff}, 9}, {[]byte{0xff, 0}, 9},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, node.ChildFor(tc.key), "key %x", tc.key)
	}
}

func TestBPlusInternal_ChildForEveryBoundary(t *testing.T) {
	for _, count := range []int{1, 2, 3, 4, 7, 8, 31, 32, 255, 256} {
		t.Run(fmt.Sprintf("Keys%d", count), func(t *testing.T) {
			keys, children := internalFixture(count)
			node, err := NewBPlusInternal(keys, children)
			require.NoError(t, err)
			for value := 0; value <= 2*count+2; value++ {
				index := value / 2
				if index > count {
					index = count
				}
				key := []byte(fmt.Sprintf("%08d", value))
				assert.Equal(t, children[index], node.ChildFor(key), "key %q", key)
			}
		})
	}
}

func internalFixture(count int) ([][]byte, []uint64) {
	keys := make([][]byte, count)
	children := make([]uint64, count+1)
	for i := range keys {
		keys[i] = []byte(fmt.Sprintf("%08d", 2*(i+1)))
	}
	for i := range children {
		children[i] = uint64(count + 1 - i)
	}
	return keys, children
}
