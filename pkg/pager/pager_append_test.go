package pager

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPager_AppendPageUsesNextIDAndPreservesExistingPages(t *testing.T) {
	for _, existingCount := range []int{0, 1, 2, 5} {
		t.Run(fmt.Sprintf("ExistingPages%d", existingCount), func(t *testing.T) {
			expected := bytes.Repeat([]byte{0x11}, existingCount*4096)
			p := newPagerForReadTest(t, expected)

			for i := 0; i < 3; i++ {
				page := bytes.Repeat([]byte{byte(0x22 + i)}, 4096)
				original := bytes.Clone(page)
				id, err := p.AppendPage(page)
				require.NoError(t, err)
				assert.Equal(t, uint64(existingCount+i), id)
				assert.Equal(t, original, page)
				expected = append(expected, original...)
				page[0] ^= 0xff
				actual, err := os.ReadFile(p.file.Name())
				require.NoError(t, err)
				assert.Equal(t, expected, actual)
				size, err := p.GetFileSize()
				require.NoError(t, err)
				assert.Equal(t, uint64(len(expected)), size)
			}
		})
	}
}

func TestPager_AppendPageRejectsIncorrectSizeWithoutChangingFile(t *testing.T) {
	for _, size := range []int{0, 1, 4095, 4097, 8192} {
		t.Run(fmt.Sprintf("Bytes%d", size), func(t *testing.T) {
			expected := bytes.Repeat([]byte{0x11}, 8192)
			p := newPagerForReadTest(t, expected)
			id, err := p.AppendPage(make([]byte, size))
			require.Error(t, err)
			assert.Zero(t, id)
			actual, err := os.ReadFile(p.file.Name())
			require.NoError(t, err)
			assert.Equal(t, expected, actual)
		})
	}
}

func TestPager_AppendPageRejectsPartialFileWithoutChangingIt(t *testing.T) {
	for _, size := range []int{1, 4095, 4097, 8191, 8193} {
		t.Run(fmt.Sprintf("FileBytes%d", size), func(t *testing.T) {
			expected := bytes.Repeat([]byte{0x11}, size)
			p := newPagerForReadTest(t, expected)
			id, err := p.AppendPage(bytes.Repeat([]byte{0x22}, 4096))
			require.Error(t, err)
			assert.Zero(t, id)
			actual, err := os.ReadFile(p.file.Name())
			require.NoError(t, err)
			assert.Equal(t, expected, actual)
		})
	}
}

func TestPager_AppendPageWithClosedFileReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.db")
	expected := bytes.Repeat([]byte{0x11}, 8192)
	require.NoError(t, os.WriteFile(path, expected, 0o600))
	p, err := OpenPager(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	require.NoError(t, p.Close())
	id, err := p.AppendPage(make([]byte, 4096))
	require.ErrorIs(t, err, os.ErrClosed)
	assert.Zero(t, id)
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, expected, actual)
}

func TestPager_AppendPageWithReadOnlyFileReturnsWriteError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.db")
	expected := bytes.Repeat([]byte{0x11}, 8192)
	require.NoError(t, os.WriteFile(path, expected, 0o600))
	file, err := os.Open(path)
	require.NoError(t, err)
	p := &Pager{file: file}
	t.Cleanup(func() { assert.NoError(t, p.Close()) })
	id, err := p.AppendPage(make([]byte, 4096))
	require.Error(t, err)

	var pathErr *os.PathError

	require.ErrorAs(t, err, &pathErr)
	assert.Equal(t, "write", pathErr.Op)
	assert.Zero(t, id)
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, expected, actual)
}

func TestPager_AppendPageContinuesAfterReopening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.db")
	p, err := CreatePager(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	first := bytes.Repeat([]byte{0x11}, 4096)
	id, err := p.AppendPage(first)
	require.NoError(t, err)
	require.Zero(t, id)
	require.NoError(t, p.Flush())
	require.NoError(t, p.Close())

	reopened, err := OpenPager(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, reopened.Close()) })
	second := bytes.Repeat([]byte{0x22}, 4096)
	id, err = reopened.AppendPage(second)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), id)
	require.NoError(t, reopened.Flush())
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, bytes.Join([][]byte{first, second}, nil), actual)
}
