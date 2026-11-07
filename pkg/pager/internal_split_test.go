package pager

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func internalSplitFixture(t testing.TB, sizes []int) *BPlusInternal {
	t.Helper()
	keys := make([][]byte, len(sizes))

	children := make([]uint64, len(sizes)+1)
	for i, size := range sizes {
		keys[i] = bytes.Repeat([]byte{byte('a' + i)}, size)
	}

	for i := range children {
		children[i] = uint64(100 + i)
	}

	node, err := NewBPlusInternal(keys, children)
	require.NoError(t, err)
	return node
}

func TestBPlusInternal_Split(t *testing.T) {
	cases := []struct {
		name     string
		sizes    []int
		promoted []int
	}{
		{"SingleChild", nil, nil},
		{"AlreadyFits", []int{3, 4, 5}, nil},
		{"EmptyKey", []int{0, 3}, nil},
		{"ExactFit", []int{2000, 2065}, nil},
		{"MaximumKey", []int{4075}, nil},
		{"OneByteOverflow", []int{2000, 2066}, []int{1}},
		{"TwoNodes", []int{2000, 2100, 100}, []int{1}},
		{"ThreeNodes", []int{3000, 3000, 3000, 3000, 3000}, []int{1, 3}},
		{"FinalSingleChild", []int{4075, 4075, 4075, 4075}, []int{1, 3}},
		{"PromotedKeyConsumesNoSpace", []int{3000, 3000, 2000, 2000}, []int{1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := internalSplitFixture(t, tc.sizes)
			before, err := NewBPlusInternal(node.keys, node.children)
			require.NoError(t, err)
			nodes, separators, err := node.Split()
			require.NoError(t, err)
			require.Len(t, nodes, len(tc.promoted)+1)
			require.Len(t, separators, len(tc.promoted))
			assert.Equal(t, before, node)

			start := 0

			parentChildren := make([]uint64, len(nodes))
			for i, part := range nodes {
				end := len(node.keys)
				if i < len(tc.promoted) {
					end = tc.promoted[i]
					assert.Equal(t, node.keys[end], separators[i])
				}

				assert.Equal(t, node.keys[start:end], part.keys)
				assert.Equal(t, node.children[start:end+1], part.children)
				require.NoError(t, ValidateInternal(part))
				page, err := EncodeInternal(part)
				require.NoError(t, err)
				decoded, err := DecodeInternal(page)
				require.NoError(t, err)
				assert.Equal(t, part, decoded)
				parentChildren[i] = uint64(i + 1)
				start = end + 1
			}

			parent, err := NewBPlusInternal(separators, parentChildren)
			require.NoError(t, err)

			queries := [][]byte{nil, {0xff}}
			for _, key := range node.keys {
				queries = append(queries, key, append(bytes.Clone(key), 0))
			}

			for i, query := range queries {
				part := nodes[parent.ChildFor(query)-1]
				assert.Equal(t, node.ChildFor(query), part.ChildFor(query), "query %d", i)
			}
		})
	}
}

func TestBPlusInternal_SplitOwnsData(t *testing.T) {
	for _, sizes := range [][]int{{3, 4}, {3000, 3000, 3000, 3000, 3000}} {
		node := internalSplitFixture(t, sizes)
		before, err := NewBPlusInternal(node.keys, node.children)
		require.NoError(t, err)
		nodes, separators, err := node.Split()
		require.NoError(t, err)

		for _, part := range nodes {
			for _, key := range part.keys {
				key[0] ^= 0xff
			}

			if len(part.keys) > 0 {
				part.keys[0] = []byte("replacement")
			}

			part.children[0] = 999
		}

		for _, key := range separators {
			key[0] ^= 0xff
		}

		assert.Equal(t, before, node)
	}
}

func TestBPlusInternal_SplitRejectsInvalidNodes(t *testing.T) {
	cases := []struct {
		name string
		node *BPlusInternal
		want error
	}{
		{"Nil", nil, ErrPagerInvalidArgument},
		{"NoChildren", &BPlusInternal{}, ErrPagerInvalidInternalNode},
		{"MissingChild", &BPlusInternal{keys: byteStrings("a"), children: []uint64{1}}, ErrPagerInvalidInternalNode},
		{"DuplicateChildren", &BPlusInternal{keys: byteStrings("a"), children: []uint64{1, 1}}, ErrPagerInvalidInternalNode},
		{"ZeroChild", &BPlusInternal{children: []uint64{0}}, ErrPagerInvalidInternalNode},
		{"ChildTooLarge", &BPlusInternal{children: []uint64{maxPageID + 1}}, ErrPagerInvalidInternalNode},
		{"DescendingKeys", &BPlusInternal{keys: byteStrings("b", "a"), children: []uint64{1, 2, 3}}, ErrPagerInvalidArgument},
		{"DuplicateKeys", &BPlusInternal{keys: byteStrings("a", "a"), children: []uint64{1, 2, 3}}, ErrPagerInvalidArgument},
		{"OversizedKey", internalSplitFixture(t, []int{4076}), ErrPagerEntryTooLarge},
		{"OversizedKeyAfterOverflow", internalSplitFixture(t, []int{3000, 3000, 4076}), ErrPagerEntryTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nodes, separators, err := tc.node.Split()
			require.ErrorIs(t, err, tc.want)
			assert.Nil(t, nodes)
			assert.Nil(t, separators)
		})
	}
}
