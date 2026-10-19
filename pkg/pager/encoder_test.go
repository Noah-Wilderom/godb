package pager

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeInternal_WritesKnownLayout(t *testing.T) {
	node, err := NewBPlusInternal(byteStrings("cat", "dog"), []uint64{1, 2, 3})
	require.NoError(t, err)
	page, err := EncodeInternal(node)
	require.NoError(t, err)
	expected := make([]byte, 4096)
	copy(expected, []byte{
		2, 2, 0, 1, 0, 0, 0, 0, 0, 0, 0,
		3, 0, 'c', 'a', 't', 2, 0, 0, 0, 0, 0, 0, 0,
		3, 0, 'd', 'o', 'g', 3, 0, 0, 0, 0, 0, 0, 0,
	})
	assert.Equal(t, expected, page)
	assert.Equal(t, byteStrings("cat", "dog"), node.keys)
	assert.Equal(t, []uint64{1, 2, 3}, node.children)
}

func knownInternalPage() []byte {
	page := make([]byte, 4096)
	copy(page, []byte{
		2, 2, 0, 1, 0, 0, 0, 0, 0, 0, 0,
		3, 0, 'c', 'a', 't', 2, 0, 0, 0, 0, 0, 0, 0,
		3, 0, 'd', 'o', 'g', 3, 0, 0, 0, 0, 0, 0, 0,
	})
	return page
}

func TestDecodeInternal_ReadsKnownLayout(t *testing.T) {
	page := knownInternalPage()
	original := bytes.Clone(page)
	node, err := DecodeInternal(page)
	require.NoError(t, err)
	assert.Equal(t, byteStrings("cat", "dog"), node.keys)
	assert.Equal(t, []uint64{1, 2, 3}, node.children)
	assert.Equal(t, original, page)
	assert.Equal(t, uint64(1), node.ChildFor([]byte("ant")))
	assert.Equal(t, uint64(2), node.ChildFor([]byte("cat")))
	assert.Equal(t, uint64(3), node.ChildFor([]byte("dog")))
}

func TestInternalEncoding_RoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		keys     [][]byte
		children []uint64
	}{
		{"SingleChild", nil, []uint64{0x010203040506}},
		{"EmptyKey", [][]byte{{}}, []uint64{1, 2}},
		{"BinaryAndUTF8", byteStrings("", "\x00\xff", "cat", "catalog", "猫", "\xff"), []uint64{7, 6, 5, 4, 3, 2, 1}},
		{"MultibyteLength", [][]byte{bytes.Repeat([]byte{'a'}, 258)}, []uint64{1, maxPageID}},
		{"ExactFit", [][]byte{bytes.Repeat([]byte{'a'}, 4075)}, []uint64{1, 2}},
		{"CombinedExactFit", [][]byte{bytes.Repeat([]byte{'a'}, 2000), bytes.Repeat([]byte{'b'}, 2065)}, []uint64{1, 2, 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := EncodeInternal(&BPlusInternal{keys: tc.keys, children: tc.children})
			require.NoError(t, err)
			node, err := DecodeInternal(page)
			require.NoError(t, err)
			require.Len(t, node.keys, len(tc.keys))
			for i := range tc.keys {
				assert.True(t, bytes.Equal(tc.keys[i], node.keys[i]))
			}
			assert.Equal(t, tc.children, node.children)
			encoded, err := EncodeInternal(node)
			require.NoError(t, err)
			assert.Equal(t, page, encoded)
		})
	}
}

func TestDecodeInternal_MultibyteCount(t *testing.T) {
	page := make([]byte, 4096)
	copy(page, []byte{2, 0, 1, 1, 0, 0, 0, 0, 0, 0, 0})
	for i := 0; i < 256; i++ {
		start := 11 + i*11
		copy(page[start:], []byte{1, 0, byte(i)})
		binary.LittleEndian.PutUint64(page[start+3:start+11], uint64(i+2))
	}
	node, err := DecodeInternal(page)
	require.NoError(t, err)
	require.Len(t, node.keys, 256)
	require.Len(t, node.children, 257)
	assert.Equal(t, uint64(1), node.children[0])
	for i := range node.keys {
		assert.Equal(t, []byte{byte(i)}, node.keys[i])
		assert.Equal(t, uint64(i+2), node.children[i+1])
	}
}

func TestDecodeInternal_RejectsInvalidPageSizes(t *testing.T) {
	for _, size := range []int{0, 1, 10, 4095, 4097, 8192} {
		t.Run(fmt.Sprintf("Bytes%d", size), func(t *testing.T) {
			node, err := DecodeInternal(make([]byte, size))
			require.ErrorIs(t, err, ErrPagerCorruptPage)
			assert.Nil(t, node)
		})
	}
}

func TestDecodeInternal_RejectsCorruptPages(t *testing.T) {
	cases := []struct {
		name   string
		mutate func([]byte)
	}{
		{"LeafType", func(p []byte) { p[0] = 1 }},
		{"UnknownType", func(p []byte) { p[0] = 255 }},
		{"CountTooLarge", func(p []byte) { binary.LittleEndian.PutUint16(p[1:3], 409) }},
		{"MaximumCount", func(p []byte) { binary.LittleEndian.PutUint16(p[1:3], 65535) }},
		{"KeyBeyondPage", func(p []byte) { binary.LittleEndian.PutUint16(p[11:13], 65535) }},
		{"KeyLeavesOnlySevenChildBytes", func(p []byte) { binary.LittleEndian.PutUint16(p[11:13], 4076) }},
		{"NextEntryAtPageEnd", func(p []byte) { binary.LittleEndian.PutUint16(p[11:13], 4075) }},
		{"NextEntryHasOneByte", func(p []byte) { binary.LittleEndian.PutUint16(p[11:13], 4074) }},
		{"NextEntryHasNineBytes", func(p []byte) { binary.LittleEndian.PutUint16(p[11:13], 4066) }},
		{"FirstChildZero", func(p []byte) { binary.LittleEndian.PutUint64(p[3:11], 0) }},
		{"LastChildZero", func(p []byte) { binary.LittleEndian.PutUint64(p[29:37], 0) }},
		{"FirstChildTooLarge", func(p []byte) { binary.LittleEndian.PutUint64(p[3:11], maxPageID+1) }},
		{"LastChildTooLarge", func(p []byte) { binary.LittleEndian.PutUint64(p[29:37], maxPageID+1) }},
		{"RepeatedChild", func(p []byte) { binary.LittleEndian.PutUint64(p[29:37], 1) }},
		{"DuplicateKeys", func(p []byte) { copy(p[26:29], "cat") }},
		{"DescendingKeys", func(p []byte) { copy(p[26:29], "ant") }},
		{"SingleChildZero", func(p []byte) {
			binary.LittleEndian.PutUint16(p[1:3], 0)
			binary.LittleEndian.PutUint64(p[3:11], 0)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := knownInternalPage()
			tc.mutate(page)
			node, err := DecodeInternal(page)
			require.ErrorIs(t, err, ErrPagerCorruptPage)
			assert.Nil(t, node)
		})
	}
}

func TestDecodeInternal_OwnsDecodedBytes(t *testing.T) {
	page := knownInternalPage()
	node, err := DecodeInternal(page)
	require.NoError(t, err)
	page[13] = 'b'
	binary.LittleEndian.PutUint64(page[3:11], 99)
	assert.Equal(t, byteStrings("cat", "dog"), node.keys)
	assert.Equal(t, []uint64{1, 2, 3}, node.children)
	node.keys[1][0] = 'f'
	node.children[2] = 4
	assert.Equal(t, []byte("dog"), page[26:29])
	assert.Equal(t, uint64(3), binary.LittleEndian.Uint64(page[29:37]))
}

func TestDecodeInternal_IgnoresUnusedPadding(t *testing.T) {
	page := knownInternalPage()
	for i := 37; i < len(page); i++ {
		page[i] = 0xff
	}
	node, err := DecodeInternal(page)
	require.NoError(t, err)
	assert.Equal(t, byteStrings("cat", "dog"), node.keys)
	assert.Equal(t, []uint64{1, 2, 3}, node.children)
}

func TestEncodeInternal_SingleChild(t *testing.T) {
	page, err := EncodeInternal(&BPlusInternal{children: []uint64{0x010203040506}})
	require.NoError(t, err)
	expected := make([]byte, 4096)
	copy(expected, []byte{2, 0, 0, 6, 5, 4, 3, 2, 1, 0, 0})
	assert.Equal(t, expected, page)
}

func TestEncodeInternal_EmptyAndBinaryKeys(t *testing.T) {
	node := &BPlusInternal{keys: [][]byte{nil, {0, 0xff}, {0xff}}, children: []uint64{4, 3, 2, 1}}
	page, err := EncodeInternal(node)
	require.NoError(t, err)
	expected := make([]byte, 4096)
	copy(expected, []byte{
		2, 3, 0, 4, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 3, 0, 0, 0, 0, 0, 0, 0,
		2, 0, 0, 0xff, 2, 0, 0, 0, 0, 0, 0, 0,
		1, 0, 0xff, 1, 0, 0, 0, 0, 0, 0, 0,
	})
	assert.Equal(t, expected, page)
}

func TestEncodeInternal_MultibyteLengthAndChildID(t *testing.T) {
	key := bytes.Repeat([]byte{'a'}, 258)
	page, err := EncodeInternal(&BPlusInternal{keys: [][]byte{key}, children: []uint64{1, 0x010203040506}})
	require.NoError(t, err)
	expected := make([]byte, 4096)
	copy(expected, []byte{2, 1, 0, 1, 0, 0, 0, 0, 0, 0, 0, 2, 1})
	copy(expected[13:271], key)
	copy(expected[271:279], []byte{6, 5, 4, 3, 2, 1, 0, 0})
	assert.Equal(t, expected, page)
}

func TestEncodeInternal_MultibyteCount(t *testing.T) {
	keys := make([][]byte, 256)
	children := make([]uint64, 257)
	for i := range keys {
		keys[i] = []byte{byte(i)}
	}
	for i := range children {
		children[i] = uint64(i + 1)
	}
	page, err := EncodeInternal(&BPlusInternal{keys: keys, children: children})
	require.NoError(t, err)
	require.Len(t, page, 4096)
	assert.Equal(t, []byte{2, 0, 1}, page[:3])
	assert.Equal(t, uint64(1), binary.LittleEndian.Uint64(page[3:11]))
	for i := range keys {
		start := 11 + i*11
		assert.Equal(t, []byte{1, 0, byte(i)}, page[start:start+3])
		assert.Equal(t, children[i+1], binary.LittleEndian.Uint64(page[start+3:start+11]))
	}
	assert.Equal(t, make([]byte, 4096-2827), page[2827:])
}

func TestEncodeInternal_ExactFit(t *testing.T) {
	key := bytes.Repeat([]byte{'k'}, 4075)
	page, err := EncodeInternal(&BPlusInternal{keys: [][]byte{key}, children: []uint64{1, 2}})
	require.NoError(t, err)
	require.Len(t, page, 4096)
	assert.Equal(t, []byte{0xeb, 0x0f}, page[11:13])
	assert.Equal(t, key, page[13:4088])
	assert.Equal(t, []byte{2, 0, 0, 0, 0, 0, 0, 0}, page[4088:])
}

func TestEncodeInternal_RejectsInvalidNodes(t *testing.T) {
	cases := []struct {
		name string
		node *BPlusInternal
		want error
	}{
		{"Nil", nil, ErrPagerInvalidArgument},
		{"NoChildren", &BPlusInternal{}, ErrPagerInvalidInternalNode},
		{"DuplicateChildren", &BPlusInternal{keys: byteStrings("a"), children: []uint64{1, 1}}, ErrPagerInvalidInternalNode},
		{"UnsortedKeys", &BPlusInternal{keys: byteStrings("b", "a"), children: []uint64{1, 2, 3}}, ErrPagerInvalidArgument},
		{"OversizedKey", &BPlusInternal{keys: [][]byte{bytes.Repeat([]byte{'a'}, 4076)}, children: []uint64{1, 2}}, ErrPagerEntryTooLarge},
		{"PageFull", &BPlusInternal{keys: [][]byte{bytes.Repeat([]byte{'a'}, 2000), bytes.Repeat([]byte{'b'}, 2066)}, children: []uint64{1, 2, 3}}, ErrPagerPageFull},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := EncodeInternal(tc.node)
			require.ErrorIs(t, err, tc.want)
			assert.Nil(t, page)
		})
	}
}

func TestEncodeInternal_OwnsPageBytes(t *testing.T) {
	node := &BPlusInternal{keys: byteStrings("cat"), children: []uint64{1, 2}}
	first, err := EncodeInternal(node)
	require.NoError(t, err)
	second, err := EncodeInternal(node)
	require.NoError(t, err)
	first[13] = 'b'
	assert.Equal(t, byte('c'), second[13])
	assert.Equal(t, byteStrings("cat"), node.keys)
	node.keys[0][1] = 'u'
	node.children[1] = 3
	assert.Equal(t, []byte("cat"), second[13:16])
	assert.Equal(t, uint64(2), binary.LittleEndian.Uint64(second[16:24]))
}

func TestEncodeLeaf_WritesKnownVariableLengthLayout(t *testing.T) {
	leaf := &BPlusLeaf{keys: byteStrings("cat", "dog"), values: byteStrings("animal", "")}
	page, err := EncodeLeaf(leaf)
	require.NoError(t, err)
	expected := make([]byte, 4096)
	copy(expected, []byte{1, 2, 0, 3, 0, 6, 0, 'c', 'a', 't', 'a', 'n', 'i', 'm', 'a', 'l', 3, 0, 0, 0, 'd', 'o', 'g'})
	assert.Equal(t, expected, page)
	assert.Equal(t, byteStrings("cat", "dog"), leaf.keys)
	assert.Equal(t, byteStrings("animal", ""), leaf.values)
}

func TestDecodeLeaf_ReadsKnownVariableLengthLayout(t *testing.T) {
	page := make([]byte, 4096)
	copy(page, []byte{1, 2, 0, 3, 0, 6, 0, 'c', 'a', 't', 'a', 'n', 'i', 'm', 'a', 'l', 3, 0, 0, 0, 'd', 'o', 'g'})
	leaf, err := DecodeLeaf(page)
	require.NoError(t, err)
	assert.Equal(t, byteStrings("cat", "dog"), leaf.keys)
	assert.Equal(t, byteStrings("animal", ""), leaf.values)
}

func TestLeafEncoding_UsesTwoByteLittleEndianLengths(t *testing.T) {
	key, value := bytes.Repeat([]byte{'k'}, 258), bytes.Repeat([]byte{'v'}, 513)
	page, err := EncodeLeaf(&BPlusLeaf{keys: [][]byte{key}, values: [][]byte{value}})
	require.NoError(t, err)
	assert.Equal(t, []byte{2, 1, 1, 2}, page[3:7])
	assert.Equal(t, key, page[7:265])
	assert.Equal(t, value, page[265:778])
	assert.Equal(t, make([]byte, 4096-778), page[778:])
	raw := make([]byte, 4096)
	copy(raw, []byte{1, 1, 0, 2, 1, 1, 2})
	copy(raw[7:265], key)
	copy(raw[265:778], value)
	leaf, err := DecodeLeaf(raw)
	require.NoError(t, err)
	assert.Equal(t, key, leaf.keys[0])
	assert.Equal(t, value, leaf.values[0])
}

func TestLeafEncoding_EmptyLeafAndEmptyEntry(t *testing.T) {
	for _, leaf := range []*BPlusLeaf{{}, {keys: [][]byte{nil}, values: [][]byte{nil}}} {
		page, err := EncodeLeaf(leaf)
		require.NoError(t, err)
		require.Len(t, page, 4096)
		assert.Equal(t, []byte{1, byte(len(leaf.keys)), 0}, page[:3])
		assert.Equal(t, make([]byte, 4093), page[3:])
		decoded, err := DecodeLeaf(page)
		require.NoError(t, err)
		require.Len(t, decoded.keys, len(leaf.keys))
		if len(leaf.keys) != 0 {
			value, _, found := decoded.Get(nil)
			assert.True(t, found)
			assert.Empty(t, value)
		}
	}
}

func TestLeafEncoding_RoundTrip(t *testing.T) {
	for _, count := range []int{0, 1, 128, 255, 256, 511} {
		t.Run(fmt.Sprintf("Entries%d", count), func(t *testing.T) {
			original := fixtureLeaf(count, 0)
			page, err := EncodeLeaf(original)
			require.NoError(t, err)
			assert.Equal(t, uint16(count), binary.LittleEndian.Uint16(page[1:3]))
			decoded, err := DecodeLeaf(page)
			require.NoError(t, err)
			assert.Equal(t, original.keys, decoded.keys)
			assert.Equal(t, original.values, decoded.values)
		})
	}
	original := &BPlusLeaf{keys: byteStrings("", "a", "ab", "猫", "\xff"), values: byteStrings("", "\x00\xff", "different length", "🐈", "last")}
	page, err := EncodeLeaf(original)
	require.NoError(t, err)
	decoded, err := DecodeLeaf(page)
	require.NoError(t, err)
	assert.Equal(t, original.keys, decoded.keys)
	assert.Equal(t, original.values, decoded.values)
}

func TestLeafEncoding_ExactPageFit(t *testing.T) {
	original := &BPlusLeaf{keys: byteStrings("cat"), values: [][]byte{bytes.Repeat([]byte{'v'}, 4086)}}
	page, err := EncodeLeaf(original)
	require.NoError(t, err)
	require.Len(t, page, 4096)
	assert.Equal(t, byte('v'), page[4095])
	decoded, err := DecodeLeaf(page)
	require.NoError(t, err)
	assert.Equal(t, original.keys, decoded.keys)
	assert.Equal(t, original.values, decoded.values)
}

func TestEncodeLeaf_RejectsInvalidLeaves(t *testing.T) {
	for _, leaf := range []*BPlusLeaf{nil,
		{keys: byteStrings("a"), values: nil},
		{keys: byteStrings("b", "a"), values: byteStrings("", "")},
		{keys: byteStrings("a", "a"), values: byteStrings("", "")},
	} {
		page, err := EncodeLeaf(leaf)
		require.Error(t, err)
		assert.Nil(t, page)
	}
	for _, tc := range []struct {
		leaf *BPlusLeaf
		want error
	}{
		{&BPlusLeaf{keys: byteStrings("cat"), values: [][]byte{make([]byte, 4087)}}, ErrPagerEntryTooLarge},
		{&BPlusLeaf{keys: byteStrings("a", "b"), values: [][]byte{make([]byte, 2500), make([]byte, 2500)}}, ErrPagerPageFull},
	} {
		page, err := EncodeLeaf(tc.leaf)
		require.ErrorIs(t, err, tc.want)
		assert.Nil(t, page)
	}
}

func TestDecodeLeaf_RejectsIncorrectPageSize(t *testing.T) {
	for _, size := range []int{0, 1, 2, 3, 4095, 4097} {
		t.Run(fmt.Sprintf("Bytes%d", size), func(t *testing.T) {
			leaf, err := DecodeLeaf(make([]byte, size))
			require.ErrorIs(t, err, ErrPagerCorruptPage)
			assert.Nil(t, leaf)
		})
	}
	leaf, err := DecodeLeaf(nil)
	require.ErrorIs(t, err, ErrPagerCorruptPage)
	assert.Nil(t, leaf)
}

func TestDecodeLeaf_RejectsCorruption(t *testing.T) {
	cases := []struct {
		name   string
		mutate func([]byte)
	}{
		{"TypeZero", func(p []byte) { p[0] = 0 }},
		{"InternalType", func(p []byte) { p[0] = 2 }},
		{"UnknownType", func(p []byte) { p[0] = 255 }},
		{"ImpossibleCount", func(p []byte) { binary.LittleEndian.PutUint16(p[1:3], 1024) }},
		{"MaximumCount", func(p []byte) { binary.LittleEndian.PutUint16(p[1:3], 65535) }},
		{"KeyPastEnd", func(p []byte) { binary.LittleEndian.PutUint16(p[3:5], 4090) }},
		{"MaximumKeyLength", func(p []byte) { binary.LittleEndian.PutUint16(p[3:5], 65535) }},
		{"ValuePastEnd", func(p []byte) { binary.LittleEndian.PutUint16(p[5:7], 4089) }},
		{"MaximumValueLength", func(p []byte) { binary.LittleEndian.PutUint16(p[5:7], 65535) }},
		{"TruncatedSecondHeader", func(p []byte) { p[1] = 2; binary.LittleEndian.PutUint16(p[5:7], 4086) }},
		{"DescendingKeys", func(p []byte) { copy(p, []byte{1, 2, 0, 1, 0, 0, 0, 'b', 1, 0, 0, 0, 'a'}) }},
		{"DuplicateKeys", func(p []byte) { copy(p, []byte{1, 2, 0, 1, 0, 0, 0, 'a', 1, 0, 0, 0, 'a'}) }},
		{"DuplicateEmptyKeys", func(p []byte) { clear(p); p[0] = 1; p[1] = 2 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := make([]byte, 4096)
			copy(page, []byte{1, 1, 0, 1, 0, 0, 0, 'a'})
			tc.mutate(page)
			leaf, err := DecodeLeaf(page)
			require.ErrorIs(t, err, ErrPagerCorruptPage)
			assert.Nil(t, leaf)
			var typed *Error
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, CodePagerCorruptPage, typed.Code)
		})
	}
}

func TestDecodeLeaf_IgnoresUnusedPaddingAndOwnsItsBytes(t *testing.T) {
	page := bytes.Repeat([]byte{255}, 4096)
	copy(page, []byte{1, 1, 0, 1, 0, 1, 0, 'a', 'v'})
	original := bytes.Clone(page)
	leaf, err := DecodeLeaf(page)
	require.NoError(t, err)
	assert.Equal(t, original, page)
	leaf.keys[0][0] = 'b'
	leaf.values[0][0] = 'w'
	assert.Equal(t, original, page)
	clear(page)
	assert.Equal(t, byteStrings("b"), leaf.keys)
	assert.Equal(t, byteStrings("w"), leaf.values)
}

func knownHeaderPage() []byte {
	page := make([]byte, 4096)
	copy(page, []byte{
		'G', 'O', 'D', 'B',
		0x01, 0x00, // Format version 1.
		0x20, 0x00, // Header length 32.
		0x00, 0x10, 0x00, 0x00, // Page size 4096.
		0x00, 0x00, 0x00, 0x00, // Reserved.
		0x06, 0x05, 0x04, 0x03, 0x02, 0x01, 0x00, 0x00, // Root page ID.
		0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x00, 0x00, // Page count.
	})
	return page
}

func TestEncodeHeaderWritesKnownBytesAndZeroPadding(t *testing.T) {
	header := &DatabaseHeader{
		FormatVersion: 1, PageSize: 4096,
		RootPageID: 0x010203040506, PageCount: 0x020304050607,
	}
	original := *header

	page, err := EncodeHeader(header)

	require.NoError(t, err)
	assert.Equal(t, knownHeaderPage(), page)
	assert.Equal(t, original, *header)
}

func TestDecodeHeaderWithKnownBytesReadsLittleEndianFields(t *testing.T) {
	page := knownHeaderPage()
	original := append([]byte(nil), page...)
	expected := &DatabaseHeader{
		FormatVersion: 1, PageSize: 4096,
		RootPageID: 0x010203040506, PageCount: 0x020304050607,
	}

	header, err := DecodeHeader(page)

	require.NoError(t, err)
	assert.Equal(t, expected, header)
	assert.Equal(t, original, page)
	clear(page)
	assert.Equal(t, expected, header)
}

func TestDecodeHeaderAfterEncodingPreservesFields(t *testing.T) {
	cases := []struct {
		name   string
		header *DatabaseHeader
	}{
		{name: "NewDatabase", header: NewDatabaseHeader()},
		{name: "RootInsideFile", header: &DatabaseHeader{FormatVersion: 1, PageSize: 4096, RootPageID: 42, PageCount: 100}},
		{name: "LargestFile", header: &DatabaseHeader{FormatVersion: 1, PageSize: 4096, RootPageID: maxPageID, PageCount: maxPageID + 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := EncodeHeader(tc.header)
			require.NoError(t, err)

			decoded, err := DecodeHeader(page)

			require.NoError(t, err)
			assert.Equal(t, tc.header, decoded)
		})
	}
}

func TestEncodeHeaderWithNilHeaderReturnsErrorAndNoPage(t *testing.T) {
	page, err := EncodeHeader(nil)

	require.Error(t, err)
	assert.Nil(t, page)
}

func TestHeaderWithInvalidFieldsReturnsError(t *testing.T) {
	cases := []struct {
		name   string
		change func(*DatabaseHeader)
	}{
		{name: "ZeroVersion", change: func(h *DatabaseHeader) { h.FormatVersion = 0 }},
		{name: "FutureVersion", change: func(h *DatabaseHeader) { h.FormatVersion = 2 }},
		{name: "ZeroPageSize", change: func(h *DatabaseHeader) { h.PageSize = 0 }},
		{name: "UnsupportedPageSize", change: func(h *DatabaseHeader) { h.PageSize = 8192 }},
		{name: "ZeroPageCount", change: func(h *DatabaseHeader) { h.PageCount = 0 }},
		{name: "HeaderOnly", change: func(h *DatabaseHeader) { h.PageCount = 1 }},
		{name: "PageCountTooLarge", change: func(h *DatabaseHeader) { h.PageCount = maxPageID + 2 }},
		{name: "MaximumUint64PageCount", change: func(h *DatabaseHeader) { h.PageCount = ^uint64(0) }},
		{name: "RootIsHeader", change: func(h *DatabaseHeader) { h.RootPageID = 0 }},
		{name: "RootEqualsPageCount", change: func(h *DatabaseHeader) { h.RootPageID = h.PageCount }},
		{name: "RootBeyondFile", change: func(h *DatabaseHeader) { h.RootPageID = h.PageCount + 1 }},
		{name: "MaximumUint64Root", change: func(h *DatabaseHeader) { h.RootPageID = ^uint64(0) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header := NewDatabaseHeader()
			tc.change(header)

			t.Run("Encode", func(t *testing.T) {
				page, err := EncodeHeader(header)
				require.Error(t, err)
				assert.Nil(t, page)
			})
			t.Run("Decode", func(t *testing.T) {
				// Build invalid bytes directly: EncodeHeader must reject them.
				page := knownHeaderPage()
				binary.LittleEndian.PutUint16(page[4:6], header.FormatVersion)
				binary.LittleEndian.PutUint32(page[8:12], header.PageSize)
				binary.LittleEndian.PutUint64(page[16:24], header.RootPageID)
				binary.LittleEndian.PutUint64(page[24:32], header.PageCount)

				decoded, err := DecodeHeader(page)
				require.Error(t, err)
				assert.Nil(t, decoded)
			})
		})
	}
}

func TestDecodeHeaderWithIncorrectPageSizeReturnsError(t *testing.T) {
	header, err := DecodeHeader(nil)
	require.Error(t, err)
	assert.Nil(t, header)

	for _, size := range []int{0, 1, 4, 31, 32, 4095, 4097} {
		t.Run(fmt.Sprintf("Bytes%d", size), func(t *testing.T) {
			page := make([]byte, size)
			copy(page, knownHeaderPage())

			header, err := DecodeHeader(page)

			require.Error(t, err)
			assert.Nil(t, header)
		})
	}
}

func TestDecodeHeaderWithInvalidSignatureReturnsError(t *testing.T) {
	for i := range 4 {
		t.Run(fmt.Sprintf("Byte%d", i), func(t *testing.T) {
			page := knownHeaderPage()
			page[i] = 0

			header, err := DecodeHeader(page)

			require.Error(t, err)
			assert.Nil(t, header)
		})
	}
}

func TestDecodeHeaderWithInvalidHeaderLengthReturnsError(t *testing.T) {
	for _, length := range []uint16{0, 31, 33, 4096, 65535} {
		t.Run(fmt.Sprintf("Length%d", length), func(t *testing.T) {
			page := knownHeaderPage()
			binary.LittleEndian.PutUint16(page[6:8], length)

			header, err := DecodeHeader(page)

			require.Error(t, err)
			assert.Nil(t, header)
		})
	}
}

func TestDecodeHeaderWithNonzeroReservedBytesReturnsError(t *testing.T) {
	page := knownHeaderPage()
	// Check every reserved position independently, including both region edges.
	for _, region := range [][2]int{{12, 16}, {32, 4096}} {
		for offset := region[0]; offset < region[1]; offset++ {
			for _, value := range []byte{0x01, 0x80, 0xff} {
				page[offset] = value

				header, err := DecodeHeader(page)

				require.Error(t, err, "reserved byte %d = %02x", offset, value)
				require.Nil(t, header, "reserved byte %d = %02x", offset, value)
			}
			page[offset] = 0
		}
	}
}
