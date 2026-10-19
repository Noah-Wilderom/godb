package database

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate_AcceptsNewDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.db")
	require.NoError(t, Create(path))
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	require.NoError(t, Validate(path))

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestValidate_AcceptsValidRootLeaves(t *testing.T) {
	for _, count := range []int{0, 1, 2, 255, 300} {
		t.Run(fmt.Sprintf("Entries%d", count), func(t *testing.T) {
			contents := databaseBytesWithEntries(fixtureEntries(count))
			path := writeDatabaseFixture(t, contents)

			require.NoError(t, Validate(path))

			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, contents, after)
		})
	}
}

func TestValidate_ReadsRootAtHeaderPageID(t *testing.T) {
	contents := append(validDatabaseBytes(), make([]byte, 4096)...)
	binary.LittleEndian.PutUint64(contents[16:24], 2)
	binary.LittleEndian.PutUint64(contents[24:32], 3)
	contents[4096] = 0xff
	contents[8192] = 0x01
	path := writeDatabaseFixture(t, contents)

	require.NoError(t, Validate(path))

	contents[8192] = 0xff
	require.NoError(t, os.WriteFile(path, contents, 0600))
	require.Error(t, Validate(path))
}

func TestValidate_RejectsEmptyPath(t *testing.T) {
	require.ErrorContains(t, Validate(""), "path must not be empty")
}

func TestValidate_WithMissingFileDoesNotCreateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")

	require.ErrorIs(t, Validate(path), os.ErrNotExist)

	_, err := os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestValidate_RejectsDirectory(t *testing.T) {
	require.Error(t, Validate(t.TempDir()))
}

func TestValidate_WithIncompleteHeaderReturnsEOF(t *testing.T) {
	for _, size := range []int{0, 1, 4, 31, 32, 4095} {
		t.Run(fmt.Sprintf("Bytes%d", size), func(t *testing.T) {
			path := writeDatabaseFixture(t, validDatabaseBytes()[:size])

			require.ErrorIs(t, Validate(path), io.EOF)
		})
	}
}

func TestValidate_RejectsFileSizeMismatch(t *testing.T) {
	cases := []struct {
		name      string
		size      int
		pageCount uint64
	}{
		{name: "MissingRoot", size: 4096, pageCount: 2},
		{name: "PartialRoot", size: 4196, pageCount: 2},
		{name: "OneByteShort", size: 8191, pageCount: 2},
		{name: "TrailingByte", size: 8193, pageCount: 2},
		{name: "ExtraPage", size: 12288, pageCount: 2},
		{name: "DeclaredPageMissing", size: 8192, pageCount: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			contents := make([]byte, tc.size)
			copy(contents, validDatabaseBytes())
			binary.LittleEndian.PutUint64(contents[24:32], tc.pageCount)
			path := writeDatabaseFixture(t, contents)

			require.ErrorContains(t, Validate(path), "file size does not match")

			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, contents, after)
		})
	}
}

func TestValidate_RejectsCorruptedHeader(t *testing.T) {
	cases := []struct {
		name   string
		change func([]byte)
	}{
		{name: "Signature", change: func(p []byte) { p[0] = 'X' }},
		{name: "ZeroVersion", change: func(p []byte) { binary.LittleEndian.PutUint16(p[4:6], 0) }},
		{name: "Version", change: func(p []byte) { binary.LittleEndian.PutUint16(p[4:6], 2) }},
		{name: "HeaderLength", change: func(p []byte) { binary.LittleEndian.PutUint16(p[6:8], 31) }},
		{name: "PageSize", change: func(p []byte) { binary.LittleEndian.PutUint32(p[8:12], 8192) }},
		{name: "ReservedHeaderByte", change: func(p []byte) { p[12] = 1 }},
		{name: "ReservedPaddingByte", change: func(p []byte) { p[4095] = 1 }},
		{name: "RootIsHeader", change: func(p []byte) { binary.LittleEndian.PutUint64(p[16:24], 0) }},
		{name: "RootOutsideFile", change: func(p []byte) { binary.LittleEndian.PutUint64(p[16:24], 2) }},
		{name: "PageCountTooSmall", change: func(p []byte) { binary.LittleEndian.PutUint64(p[24:32], 1) }},
		{name: "PageCountOverflow", change: func(p []byte) { binary.LittleEndian.PutUint64(p[24:32], ^uint64(0)) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			contents := validDatabaseBytes()
			tc.change(contents)
			path := writeDatabaseFixture(t, contents)

			require.Error(t, Validate(path))

			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, contents, after)
		})
	}
}

func TestValidate_RejectsCorruptedRootLeaf(t *testing.T) {
	cases := []struct {
		name   string
		change func([]byte)
	}{
		{name: "ZeroPageType", change: func(p []byte) { p[0] = 0 }},
		{name: "LeafBytesMarkedAsInternal", change: func(p []byte) { p[0] = 2 }},
		{name: "UnknownPageType", change: func(p []byte) { p[0] = 0xff }},
		{name: "TooManyEntries", change: func(p []byte) { binary.LittleEndian.PutUint16(p[1:3], 1024) }},
		{name: "KeyPastEnd", change: func(p []byte) { copy(p, []byte{1, 1, 0, 255, 255, 0, 0}) }},
		{name: "ValuePastEnd", change: func(p []byte) { copy(p, []byte{1, 1, 0, 0, 0, 255, 255}) }},
		{name: "DuplicateKeys", change: func(p []byte) { copy(p, []byte{1, 2, 0, 1, 0, 0, 0, 'a', 1, 0, 0, 0, 'a'}) }},
		{name: "DescendingKeys", change: func(p []byte) { copy(p, []byte{1, 2, 0, 1, 0, 0, 0, 'b', 1, 0, 0, 0, 'a'}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			contents := validDatabaseBytes()
			tc.change(contents[4096:])
			path := writeDatabaseFixture(t, contents)

			require.Error(t, Validate(path))

			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, contents, after)
		})
	}
}

func validDatabaseBytes() []byte {
	contents := make([]byte, 8192)
	copy(contents, []byte{
		'G', 'O', 'D', 'B',
		1, 0,
		32, 0,
		0, 16, 0, 0,
		0, 0, 0, 0,
		1, 0, 0, 0, 0, 0, 0, 0,
		2, 0, 0, 0, 0, 0, 0, 0,
	})
	contents[4096] = 1
	return contents
}

func writeDatabaseFixture(t testing.TB, contents []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "database.db")
	require.NoError(t, os.WriteFile(path, contents, 0600))
	return path
}
