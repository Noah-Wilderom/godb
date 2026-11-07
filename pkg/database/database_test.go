package database

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/pager"
)

func TestCreate_WritesHeaderAndEmptyRootLeaf(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.db")
	require.NoError(t, Create(path))
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, validDatabaseBytes(), contents)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Zero(t, info.Mode().Perm()&0o077)
	file, err := pager.OpenPager(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, file.Close()) })
	page, err := file.ReadPage(0)
	require.NoError(t, err)
	header, err := pager.DecodeHeader(page)
	require.NoError(t, err)
	assert.Equal(t, uint16(1), header.FormatVersion)
	assert.Equal(t, pager.NewDatabaseHeader(), header)
	page, err = file.ReadPage(header.RootPageID)
	require.NoError(t, err)
	leaf, err := pager.DecodeLeaf(page)
	require.NoError(t, err)
	_, _, found := leaf.Get(nil)
	assert.False(t, found)
}

func TestCreate_RefusesToOverwriteExistingFile(t *testing.T) {
	contents := []byte("preserve existing data")
	path := writeDatabaseFixture(t, contents)
	require.ErrorIs(t, Create(path), os.ErrExist)
	assertFileContents(t, path, contents)
}

func TestCreate_RejectsEmptyPath(t *testing.T) {
	require.ErrorContains(t, Create(""), "path must not be empty")
}

func TestCreate_WithMissingParentReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "database.db")
	require.ErrorIs(t, Create(path), os.ErrNotExist)
	_, err := os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestOpen_AcceptsCreatedDatabaseAndKeepsItOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.db")
	require.NoError(t, Create(path))
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	value, found, err := db.Get([]byte("missing"))
	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, value)
}

func TestOpen_RejectsInvalidPaths(t *testing.T) {
	for _, path := range []string{"", t.TempDir(), filepath.Join(t.TempDir(), "missing.db")} {
		db, err := Open(path)
		require.Error(t, err)
		assert.Nil(t, db)
	}

	path := filepath.Join(t.TempDir(), "absent.db")
	db, err := Open(path)
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.Nil(t, db)
	_, err = os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestOpen_RejectsInvalidDatabaseWithoutChangingContents(t *testing.T) {
	cases := []struct {
		name   string
		change func([]byte) []byte
	}{
		{"Empty", func(p []byte) []byte { return p[:0] }},
		{"TruncatedHeader", func(p []byte) []byte { return p[:100] }},
		{"InvalidSignature", func(p []byte) []byte { p[0] = 'X'; return p }},
		{"ZeroVersion", func(p []byte) []byte { p[4] = 0; return p }},
		{"FutureVersion", func(p []byte) []byte { p[4] = 2; return p }},
		{"ReservedBytes", func(p []byte) []byte { p[4095] = 1; return p }},
		{"MissingRoot", func(p []byte) []byte { return p[:4096] }},
		{"TrailingByte", func(p []byte) []byte { return append(p, 0) }},
		{"InvalidRootID", func(p []byte) []byte { p[16] = 2; return p }},
		{"InvalidRootPage", func(p []byte) []byte { p[4096] = 255; return p }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			contents := tc.change(validDatabaseBytes())
			path := writeDatabaseFixture(t, contents)

			db, err := Open(path)
			if db != nil {
				t.Cleanup(func() { _ = db.Close() })
			}

			require.Error(t, err)
			assert.Nil(t, db)
			assertFileContents(t, path, contents)
		})
	}
}

func TestDatabase_GetFindsVariableKeysAndEmptyValues(t *testing.T) {
	entries := []testEntry{record("", "empty key"), record("10", "ten"), record("2", ""), record("cat", "animal"), record("catalog", "longer value"), record("猫", "🐈"), record("\xff", "\x00\xff")}
	contents := databaseBytesWithEntries(entries)
	path := writeDatabaseFixture(t, contents)
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	for _, entry := range entries {
		value, found, err := db.Get(entry.key)
		require.NoError(t, err)
		require.True(t, found, "key %q", entry.key)
		assert.True(t, bytes.Equal(entry.value, value))
	}

	for _, key := range []string{"1", "11", "3", "ca", "cats", "\xff\xff"} {
		value, found, err := db.Get([]byte(key))
		require.NoError(t, err)
		assert.False(t, found)
		assert.Nil(t, value)
	}

	value, found, err := db.Get([]byte("cat"))
	require.NoError(t, err)
	require.True(t, found)
	value[0] = 'X'
	again, _, err := db.Get([]byte("cat"))
	require.NoError(t, err)
	assert.Equal(t, []byte("animal"), again)
	assertFileContents(t, path, contents)
}

func TestDatabase_GetWithManyEntries(t *testing.T) {
	entries := fixtureEntries(300)
	db, err := Open(writeDatabaseFixture(t, databaseBytesWithEntries(entries)))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	for _, entry := range entries {
		value, found, err := db.Get(entry.key)
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, entry.value, value)
	}
}

func TestDatabase_GetUsesHeaderRootPageID(t *testing.T) {
	contents := databaseBytesWithEntries([]testEntry{record("a", "right")})
	contents = append(contents, contents[4096:8192]...)
	binary.LittleEndian.PutUint64(contents[16:24], 2)
	binary.LittleEndian.PutUint64(contents[24:32], 3)
	copy(contents[4096:], databaseBytesWithEntries([]testEntry{record("a", "wrong")})[4096:])
	db, err := Open(writeDatabaseFixture(t, contents))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	value, found, err := db.Get([]byte("a"))
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, []byte("right"), value)
}

func TestDatabase_GetReturnsReadErrorAfterTruncation(t *testing.T) {
	path := writeDatabaseFixture(t, databaseBytesWithEntries([]testEntry{record("a", "value")}))
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	require.NoError(t, os.Truncate(path, 4196))
	value, found, err := db.Get([]byte("a"))
	require.ErrorIs(t, err, io.EOF)
	assert.False(t, found)
	assert.Nil(t, value)
}

func TestDatabase_GetReturnsDecodeErrorAfterRootCorruption(t *testing.T) {
	contents := validDatabaseBytes()
	path := writeDatabaseFixture(t, contents)
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	contents[4096] = 255
	require.NoError(t, os.WriteFile(path, contents, 0o600))
	value, found, err := db.Get([]byte("a"))
	require.ErrorIs(t, err, pager.ErrPagerCorruptPage)
	assert.False(t, found)
	assert.Nil(t, value)
}

func TestDatabase_ClosePreventsReadsAndPreservesFile(t *testing.T) {
	contents := databaseBytesWithEntries([]testEntry{record("a", "value")})
	path := writeDatabaseFixture(t, contents)
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Close())
	value, found, err := db.Get([]byte("a"))
	require.ErrorIs(t, err, ErrClosed)
	assert.False(t, found)
	assert.Nil(t, value)
	assert.NoError(t, db.Close())
	assertFileContents(t, path, contents)
	reopened, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, reopened.Close()) })
	value, found, err = reopened.Get([]byte("a"))
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, []byte("value"), value)
}

type testEntry struct{ key, value []byte }

func record(key, value string) testEntry { return testEntry{[]byte(key), []byte(value)} }

func fixtureEntries(count int) []testEntry {
	entries := make([]testEntry, count)
	for i := range entries {
		entries[i] = record(fmt.Sprintf("%04d", i*2), fmt.Sprintf("%04d", i+1000))
	}

	return entries
}

func databaseBytesWithEntries(entries []testEntry) []byte {
	contents := validDatabaseBytes()
	root := contents[4096:]
	binary.LittleEndian.PutUint16(root[1:3], uint16(len(entries)))

	offset := 3
	for _, entry := range entries {
		binary.LittleEndian.PutUint16(root[offset:offset+2], uint16(len(entry.key)))
		binary.LittleEndian.PutUint16(root[offset+2:offset+4], uint16(len(entry.value)))
		offset += 4
		copy(root[offset:offset+len(entry.key)], entry.key)
		offset += len(entry.key)
		copy(root[offset:offset+len(entry.value)], entry.value)
		offset += len(entry.value)
	}

	return contents
}

func assertFileContents(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}
