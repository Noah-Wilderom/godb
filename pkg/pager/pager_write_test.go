package pager

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPager_WritePageToEmptyFileStoresCompletePage(t *testing.T) {
	pager := newPagerForReadTest(t, nil)
	expected := bytes.Repeat([]byte{0x12, 0x34, 0x56, 0x78}, 1024)
	page := append([]byte(nil), expected...)

	require.NoError(t, pager.WritePage(0, page))

	// Check the file independently of ReadPage so matching offset bugs cannot hide.
	contents, err := os.ReadFile(pager.file.Name())
	require.NoError(t, err)
	assert.Equal(t, expected, contents)
	assert.Equal(t, expected, page, "writing must not modify the caller's buffer")
	readBack, err := pager.ReadPage(0)
	require.NoError(t, err)
	assert.Equal(t, expected, readBack)
}

func TestPager_WritePageAtEndAppendsWithoutChangingFirstPage(t *testing.T) {
	first := bytes.Repeat([]byte{0x11}, 4096)
	second := bytes.Repeat([]byte{0x22}, 4096)
	pager := newPagerForReadTest(t, first)

	require.NoError(t, pager.WritePage(1, second))

	contents, err := os.ReadFile(pager.file.Name())
	require.NoError(t, err)
	assert.Equal(t, bytes.Join([][]byte{first, second}, nil), contents)
	readBack, err := pager.ReadPage(1)
	require.NoError(t, err)
	assert.Equal(t, second, readBack)
}

func TestPager_WritePageWithExistingPagePreservesNeighbors(t *testing.T) {
	first := bytes.Repeat([]byte{0x11}, 4096)
	middle := bytes.Repeat([]byte{0x22}, 4096)
	last := bytes.Repeat([]byte{0x33}, 4096)
	pager := newPagerForReadTest(t, bytes.Join([][]byte{first, middle, last}, nil))
	// Zero bytes must replace old content just like any other bytes.
	replacement := make([]byte, 4096)

	require.NoError(t, pager.WritePage(1, replacement))

	contents, err := os.ReadFile(pager.file.Name())
	require.NoError(t, err)
	assert.Equal(t, bytes.Join([][]byte{first, replacement, last}, nil), contents)
}

func TestPager_WritePageWithIncorrectSizeLeavesFileUnchanged(t *testing.T) {
	cases := []struct {
		name string
		page []byte
	}{
		{name: "Nil"},
		{name: "Empty", page: []byte{}},
		{name: "SingleByte", page: []byte{0xff}},
		{name: "OneByteShort", page: bytes.Repeat([]byte{0xff}, 4095)},
		{name: "OneByteTooLong", page: bytes.Repeat([]byte{0xff}, 4097)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expected := bytes.Repeat([]byte{0x11}, 8192)
			pager := newPagerForReadTest(t, expected)
			// Invalid buffers must neither overwrite existing data nor extend the file.
			for _, pageID := range []uint64{0, 2} {
				err := pager.WritePage(pageID, tc.page)

				require.Error(t, err)
				contents, err := os.ReadFile(pager.file.Name())
				require.NoError(t, err)
				assert.Equal(t, expected, contents)
			}
		})
	}
}

func TestPager_WritePageWithUnsupportedIDLeavesFileUnchanged(t *testing.T) {
	cases := []struct {
		name   string
		pageID uint64
	}{
		{name: "JustAboveMaximum", pageID: uint64(maxPageID) + 1},
		{name: "MultiplicationWouldWrapToZero", pageID: uint64(1) << 52},
		{name: "MaximumUint64", pageID: ^uint64(0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expected := bytes.Repeat([]byte{0x11}, 4096)
			pager := newPagerForReadTest(t, expected)
			replacement := bytes.Repeat([]byte{0xff}, 4096)

			err := pager.WritePage(tc.pageID, replacement)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "exceeds the supported range")
			contents, err := os.ReadFile(pager.file.Name())
			require.NoError(t, err)
			assert.Equal(t, expected, contents)
		})
	}
}

func TestPager_WritePageWithClosedFileReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.db")
	expected := bytes.Repeat([]byte{0x11}, 4096)
	require.NoError(t, os.WriteFile(path, expected, 0600))
	pager, err := OpenPager(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pager.file.Close() })
	require.NoError(t, pager.Close())

	err = pager.WritePage(0, bytes.Repeat([]byte{0xff}, 4096))

	require.ErrorIs(t, err, os.ErrClosed)
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, expected, contents)
}

func TestPager_WriteEncodedLeafCanBeDecodedAfterReopening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.db")
	original, err := NewBPlusLeaf(
		byteStrings("", "10", "20", "\xff"),
		byteStrings("\x00\xff", "hundred", "", "forty-two"),
	)
	require.NoError(t, err)

	page, err := EncodeLeaf(original)
	require.NoError(t, err)
	writer, err := CreatePager(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = writer.file.Close() })
	require.NoError(t, writer.WritePage(0, page))
	require.NoError(t, writer.Close())

	// Reopening tests ordinary file persistence, not survival of a crash.
	reader, err := OpenPager(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, reader.Close()) })
	storedPage, err := reader.ReadPage(0)
	require.NoError(t, err)
	decoded, err := DecodeLeaf(storedPage)

	require.NoError(t, err)
	require.NotNil(t, decoded)
	assert.Equal(t, page, storedPage)
	assert.Equal(t, original.keys, decoded.keys)
	assert.Equal(t, original.values, decoded.values)
	for i, key := range original.keys {
		value, idx, found := decoded.Get(key)
		assert.True(t, found)
		assert.Equal(t, original.values[i], value)
		assert.Equal(t, i, idx)
	}
}
