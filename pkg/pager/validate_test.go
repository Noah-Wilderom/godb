package pager

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateInternal_RejectsInvalidStructure(t *testing.T) {
	cases := []struct {
		name string
		node *BPlusInternal
		want error
	}{
		{"Nil", nil, ErrPagerInvalidArgument},
		{"NoChildren", &BPlusInternal{}, ErrPagerInvalidInternalNode},
		{"TooFewChildren", &BPlusInternal{keys: byteStrings("a"), children: []uint64{1}}, ErrPagerInvalidInternalNode},
		{"TooManyChildren", &BPlusInternal{children: []uint64{1, 2}}, ErrPagerInvalidInternalNode},
		{"Descending", &BPlusInternal{keys: byteStrings("b", "a"), children: []uint64{1, 2, 3}}, ErrPagerInvalidArgument},
		{"DuplicateKeys", &BPlusInternal{keys: byteStrings("a", "a"), children: []uint64{1, 2, 3}}, ErrPagerInvalidArgument},
		{"DuplicateChildren", &BPlusInternal{keys: byteStrings("a", "b"), children: []uint64{1, 2, 1}}, ErrPagerInvalidInternalNode},
		{"PageZero", &BPlusInternal{children: []uint64{0}}, ErrPagerInvalidInternalNode},
		{"PageIDTooLarge", &BPlusInternal{children: []uint64{maxPageID + 1}}, ErrPagerInvalidInternalNode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorIs(t, ValidateInternal(tc.node), tc.want)
		})
	}
}

func TestValidateInternal_PageCapacity(t *testing.T) {
	cases := []struct {
		name    string
		lengths []int
		want    error
	}{
		{"SingleChild", nil, nil},
		{"EmptyKey", []int{0}, nil},
		{"VariableLengths", []int{1, 100, 300}, nil},
		{"SingleExactFit", []int{4075}, nil},
		{"CombinedExactFit", []int{2000, 2065}, nil},
		{"CombinedOneByteOver", []int{2000, 2066}, ErrPagerPageFull},
		{"NoRoomForNextEntry", []int{4075, 1}, ErrPagerPageFull},
		{"SingleKeyTooLarge", []int{4076}, ErrPagerEntryTooLarge},
		{"OversizedKeyAfterFullPage", []int{2000, 2100, 4076}, ErrPagerEntryTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keys := make([][]byte, len(tc.lengths))
			children := make([]uint64, len(keys)+1)
			for i, length := range tc.lengths {
				keys[i] = bytes.Repeat([]byte{byte('a' + i)}, length)
			}
			for i := range children {
				children[i] = uint64(i + 1)
			}
			node, err := NewBPlusInternal(keys, children)
			require.NoError(t, err)
			err = ValidateInternal(node)
			if tc.want == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.want)
			}
		})
	}
}

func TestValidateInternal_ManySmallKeys(t *testing.T) {
	for _, count := range []int{340, 341} {
		keys := make([][]byte, count)
		children := make([]uint64, count+1)
		for i := range keys {
			keys[i] = []byte{byte(i >> 8), byte(i)}
		}
		for i := range children {
			children[i] = uint64(i + 1)
		}
		node, err := NewBPlusInternal(keys, children)
		require.NoError(t, err)
		if count == 340 {
			require.NoError(t, ValidateInternal(node))
		} else {
			require.ErrorIs(t, ValidateInternal(node), ErrPagerPageFull)
		}
	}
}

func TestValidateLeaf_RejectsInvalidStructure(t *testing.T) {
	cases := []*BPlusLeaf{
		nil,
		{keys: byteStrings("a"), values: nil},
		{keys: nil, values: byteStrings("value")},
		{keys: byteStrings("b", "a"), values: byteStrings("", "")},
		{keys: byteStrings("a", "a"), values: byteStrings("", "")},
		{keys: [][]byte{nil, {}}, values: byteStrings("", "")},
	}
	for _, leaf := range cases {
		require.Error(t, ValidateLeaf(leaf))
	}
}

func TestValidateLeaf_AcceptsVariableLengthsAndExactFits(t *testing.T) {
	cases := []struct {
		name string
		leaf *BPlusLeaf
	}{
		{"Empty", &BPlusLeaf{}},
		{"EmptyEntry", &BPlusLeaf{keys: [][]byte{nil}, values: [][]byte{nil}}},
		{"DifferentLengths", &BPlusLeaf{keys: byteStrings("a", "longer-key"), values: byteStrings("long value", "")}},
		{"UnsortedValues", &BPlusLeaf{keys: byteStrings("a", "b"), values: byteStrings("z", "a")}},
		{"ExactFit", &BPlusLeaf{keys: byteStrings("cat"), values: [][]byte{make([]byte, 4086)}}},
		{"MaximumKey", &BPlusLeaf{keys: [][]byte{make([]byte, 4075)}, values: [][]byte{nil}}},
		{"MaximumKeyExactFit", &BPlusLeaf{keys: [][]byte{make([]byte, 4075)}, values: [][]byte{make([]byte, 14)}}},
		{"MoreThan255Entries", fixtureLeaf(256, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { require.NoError(t, ValidateLeaf(tc.leaf)) })
	}
}

func TestValidateLeaf_ReportsOversizedEntryBeforePageFull(t *testing.T) {
	cases := []struct {
		name string
		leaf *BPlusLeaf
	}{
		{"Key", &BPlusLeaf{keys: [][]byte{make([]byte, 4090)}, values: [][]byte{nil}}},
		{"KeyOneByteOverLimit", &BPlusLeaf{keys: [][]byte{make([]byte, 4076)}, values: [][]byte{nil}}},
		{"MaximumKeyWithValueOneByteOver", &BPlusLeaf{keys: [][]byte{make([]byte, 4075)}, values: [][]byte{make([]byte, 15)}}},
		{"Value", &BPlusLeaf{keys: [][]byte{nil}, values: [][]byte{make([]byte, 4090)}}},
		{"Combined", &BPlusLeaf{keys: byteStrings("cat"), values: [][]byte{make([]byte, 4087)}}},
		{"LaterOversizedEntry", &BPlusLeaf{keys: byteStrings("a", "b", "c"), values: [][]byte{make([]byte, 2500), make([]byte, 2500), make([]byte, 4090)}}},
		{"LaterOversizedKey", &BPlusLeaf{keys: [][]byte{[]byte("a"), []byte("b"), bytes.Repeat([]byte{'c'}, 4076)}, values: [][]byte{make([]byte, 2500), make([]byte, 2500), nil}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLeaf(tc.leaf)
			require.ErrorIs(t, err, ErrPagerEntryTooLarge)
			require.NotErrorIs(t, err, ErrPagerPageFull)
		})
	}
}

func TestValidateLeaf_ReportsPageFullAtEachCapacityCheck(t *testing.T) {
	cases := []struct {
		name                   string
		firstValueSize         int
		secondKey, secondValue []byte
	}{
		{"EntryHeader", 4088, []byte("b"), nil},
		{"Key", 4084, []byte("bb"), nil},
		{"Value", 4083, []byte("b"), []byte("v")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaf := &BPlusLeaf{keys: [][]byte{[]byte("a"), tc.secondKey}, values: [][]byte{bytes.Repeat([]byte{'v'}, tc.firstValueSize), tc.secondValue}}
			err := ValidateLeaf(leaf)
			require.ErrorIs(t, err, ErrPagerPageFull)
			require.NotErrorIs(t, err, ErrPagerEntryTooLarge)
		})
	}
}
