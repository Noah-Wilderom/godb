package pager

import (
	"bytes"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPageOffset_Bounds(t *testing.T) {
	for _, id := range []uint64{0, 1, maxPageID} {
		offset, err := pageOffset(id)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, offset, int64(0))
		assert.LessOrEqual(t, offset, int64(math.MaxInt64-pagerByteSize))
		assert.Equal(t, id, uint64(offset/pagerByteSize))
	}

	for _, id := range []uint64{maxPageID + 1, 1 << 52, math.MaxUint64} {
		offset, err := pageOffset(id)
		require.Error(t, err)
		assert.Zero(t, offset)
	}
}

func TestCreatePager_WithMissingFileCreatesEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.db")

	pager, err := CreatePager(path)

	require.NoError(t, err)
	require.NotNil(t, pager)
	t.Cleanup(func() { assert.NoError(t, pager.Close()) })
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Zero(t, info.Size())
	assert.True(t, info.Mode().IsRegular())
	assert.Zero(t, info.Mode().Perm()&0o077, "new files should not grant group or other permissions")
}

func TestOpenPager_WithExistingFilePreservesContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.db")
	expected := bytes.Repeat([]byte{0x12, 0x34, 0x56, 0x78}, 1024)
	require.NoError(t, os.WriteFile(path, expected, 0o600))

	pager, err := OpenPager(path)

	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, pager.Close()) })
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, expected, actual)
}

func TestOpenPager_WithMissingParentDirectoryReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "database.db")

	pager, err := OpenPager(path)

	require.ErrorIs(t, err, os.ErrNotExist)
	assert.Nil(t, pager)
}

func TestPager_CloseClosesFileAndPreservesContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.db")
	expected := bytes.Repeat([]byte{0x42}, 4096)
	require.NoError(t, os.WriteFile(path, expected, 0o600))
	pager, err := OpenPager(path)
	require.NoError(t, err)
	// Best-effort cleanup if an assertion exits before the explicit close.
	t.Cleanup(func() { _ = pager.file.Close() })

	require.NoError(t, pager.Close())

	_, err = pager.file.Stat()
	assert.ErrorIs(t, err, os.ErrClosed)
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, expected, actual)
}

func TestPager_CloseWithAlreadyClosedFileReturnsError(t *testing.T) {
	pager, err := CreatePager(filepath.Join(t.TempDir(), "database.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = pager.file.Close() })
	require.NoError(t, pager.Close())

	assert.ErrorIs(t, pager.Close(), os.ErrClosed)
}

func TestPager_ReadPageWithMultiplePagesReadsRequestedPage(t *testing.T) {
	pages := [][]byte{
		bytes.Repeat([]byte{0x11}, 4096),
		bytes.Repeat([]byte{0x22}, 4096),
		bytes.Repeat([]byte{0x33}, 4096),
	}
	pager := newPagerForReadTest(t, bytes.Join(pages, nil))

	// Read out of order and repeat a read to verify addressing by page ID.
	// The last page also checks that a full read ending at EOF succeeds.
	for _, pageID := range []uint64{2, 0, 1, 1} {
		page, err := pager.ReadPage(pageID)

		require.NoError(t, err)
		require.Len(t, page, 4096)
		assert.Equal(t, pages[pageID], page)
	}
}

func TestPager_ReadPageWithEmptyFileReturnsEOF(t *testing.T) {
	pager := newPagerForReadTest(t, nil)

	page, err := pager.ReadPage(0)

	require.ErrorIs(t, err, io.EOF)
	assert.Nil(t, page)
}

func TestPager_ReadPageAtOrBeyondEndReturnsEOF(t *testing.T) {
	pager := newPagerForReadTest(t, bytes.Repeat([]byte{0x11}, 4096))

	for _, pageID := range []uint64{1, 4} {
		page, err := pager.ReadPage(pageID)

		require.ErrorIs(t, err, io.EOF)
		assert.Nil(t, page)
	}
}

func TestPager_ReadPageWithTruncatedPageReturnsEOFAndNoPartialData(t *testing.T) {
	pager := newPagerForReadTest(t, bytes.Repeat([]byte{0x11}, 100))

	page, err := pager.ReadPage(0)

	require.ErrorIs(t, err, io.EOF)
	assert.Nil(t, page)
}

func TestPager_ReadPageWithPartialFinalPagePreservesCompletePage(t *testing.T) {
	completePage := bytes.Repeat([]byte{0x11}, 4096)
	contents := bytes.Join([][]byte{completePage, bytes.Repeat([]byte{0x22}, 100)}, nil)
	pager := newPagerForReadTest(t, contents)

	page, err := pager.ReadPage(0)
	require.NoError(t, err)
	assert.Equal(t, completePage, page)

	page, err = pager.ReadPage(1)
	require.ErrorIs(t, err, io.EOF)
	assert.Nil(t, page)
}

func TestPager_ReadPageWithUnsupportedIDReturnsError(t *testing.T) {
	// Page zero exists, so a wrapped offset of zero must not succeed.
	pager := newPagerForReadTest(t, bytes.Repeat([]byte{0x11}, 4096))

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
			page, err := pager.ReadPage(tc.pageID)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "exceeds the supported range")
			assert.Nil(t, page)
		})
	}
}

func TestPager_ReadPageWithClosedFileReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.db")
	require.NoError(t, os.WriteFile(path, make([]byte, 4096), 0o600))
	pager, err := OpenPager(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pager.file.Close() })
	require.NoError(t, pager.Close())

	page, err := pager.ReadPage(0)

	require.ErrorIs(t, err, os.ErrClosed)
	assert.Nil(t, page)
}

func TestPager_ReadPageReturnsIndependentBuffers(t *testing.T) {
	expected := bytes.Repeat([]byte{0x11}, 4096)
	pager := newPagerForReadTest(t, expected)
	first, err := pager.ReadPage(0)
	require.NoError(t, err)
	second, err := pager.ReadPage(0)
	require.NoError(t, err)

	first[0] = 0xff

	assert.Equal(t, expected, second)
	again, err := pager.ReadPage(0)
	require.NoError(t, err)
	assert.Equal(t, expected, again)
}

// Create fixtures through os.WriteFile so setup does not depend on WritePage.
// Every test gets its own temporary file.
func newPagerForReadTest(t *testing.T, contents []byte) *Pager {
	t.Helper()
	path := filepath.Join(t.TempDir(), "database.db")
	require.NoError(t, os.WriteFile(path, contents, 0o600))
	pager, err := OpenPager(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, pager.Close()) })
	return pager
}

func TestOpenPager_WithMissingFileReturnsErrorWithoutCreatingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.db")

	pager, err := OpenPager(path)

	require.ErrorIs(t, err, os.ErrNotExist)
	assert.Nil(t, pager)
	_, err = os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestCreatePager_WithExistingFilePreservesContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.db")
	expected := []byte("existing database contents")
	require.NoError(t, os.WriteFile(path, expected, 0o600))

	pager, err := CreatePager(path)

	require.ErrorIs(t, err, os.ErrExist)
	assert.Nil(t, pager)
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, expected, actual)
}
