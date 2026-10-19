package database

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"testing"

	"github.com/Noah-Wilderom/godb/pkg/pager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDatabase_PutThroughInternalNodesChangesOnlyTargetLeaf(t *testing.T) {
	cases := []struct {
		name   string
		key    string
		pageID int
	}{
		{"InsertBeforeFirstKey", "", 1},
		{"InsertBelowSeparator", "car", 1},
		{"InsertBetweenSeparators", "cow", 2},
		{"InsertInThirdLeaf", "wolf", 3},
		{"InsertAfterLastKey", "zz", 4},
		{"ReplaceFirstLeaf", "ant", 1},
		{"ReplaceLowerSeparator", "cat", 2},
		{"ReplaceRootSeparator", "dog", 3},
		{"ReplaceRightSeparator", "yak", 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture, path := newTraversalDatabase(t, 7, traversalPages(t))
			require.NoError(t, fixture.Close())
			db, err := Open(path)
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			value := []byte("new value with a different length")
			require.NoError(t, db.Put([]byte(tc.key), value))
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Len(t, after, len(before))
			for pageID := 0; pageID < 8; pageID++ {
				start, end := pageID*4096, (pageID+1)*4096
				if pageID == tc.pageID {
					assert.NotEqual(t, before[start:end], after[start:end])
					leaf, err := pager.DecodeLeaf(after[start:end])
					require.NoError(t, err)
					got, _, found := leaf.Get([]byte(tc.key))
					require.True(t, found)
					assert.Equal(t, value, got)
				} else {
					assert.Equal(t, before[start:end], after[start:end], "page %d", pageID)
				}
			}
			assert.Equal(t, uint64(7), db.header.RootPageID)
			assert.Equal(t, uint64(8), db.header.PageCount)
			require.NoError(t, db.Close())
			require.NoError(t, Validate(path))
			reopened, err := Open(path)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, reopened.Close()) })
			for _, key := range []string{"ant", "bee", "cat", "catalog", "dog", "fox", "yak", "zebra", tc.key} {
				want := []byte("value:" + key)
				if key == tc.key {
					want = value
				}
				got, found, err := reopened.Get([]byte(key))
				require.NoError(t, err)
				require.True(t, found, "key %q", key)
				assert.Equal(t, want, got)
			}
		})
	}
}

func TestDatabase_PutThroughInternalNodesPreservesFileOnEncodingFailure(t *testing.T) {
	cases := []struct {
		name       string
		key, value []byte
		want       error
	}{
		{"OversizedKey", bytes.Repeat([]byte{'c'}, 4076), nil, pager.ErrPagerEntryTooLarge},
		{"OversizedValue", []byte("cat"), make([]byte, 4087), pager.ErrPagerEntryTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture, path := newTraversalDatabase(t, 7, traversalPages(t))
			require.NoError(t, fixture.Close())
			db, err := Open(path)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, db.Close()) })
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			headerBefore := *db.header
			require.ErrorIs(t, db.Put(tc.key, tc.value), tc.want)
			assertFileContents(t, path, before)
			assert.Equal(t, headerBefore, *db.header)
			got, found, err := db.Get(tc.key)
			require.NoError(t, err)
			if bytes.Equal(tc.key, []byte("cat")) {
				require.True(t, found)
				assert.Equal(t, []byte("value:cat"), got)
			} else {
				assert.False(t, found)
				assert.Nil(t, got)
			}
		})
	}
}

func TestDatabase_PutThroughInternalNodesRejectsCycleWithoutWriting(t *testing.T) {
	fixture, path := newTraversalDatabase(t, 7, traversalPages(t))
	require.NoError(t, fixture.Close())
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	page, err := db.pager.ReadPage(5)
	require.NoError(t, err)
	binary.LittleEndian.PutUint64(page[3:11], 7)
	require.NoError(t, db.pager.WritePage(5, page))
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	require.ErrorIs(t, db.Put([]byte("ant"), []byte("replacement")), pager.ErrPagerCorruptPage)
	assertFileContents(t, path, before)
}

func TestDatabase_PutInsertsAndReplacesEntriesAfterReopening(t *testing.T) {
	path := writeDatabaseFixture(t, validDatabaseBytes())
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	writes := []testEntry{record("cat", "animal"), record("2", "two"), record("10", "ten"), record("", "empty key"), record("猫", "🐈"), record("\xff", "\x00\xff"), record("cat", "a longer replacement"), record("2", "")}
	for _, entry := range writes {
		require.NoError(t, db.Put(entry.key, entry.value))
		value, found, err := db.Get(entry.key)
		require.NoError(t, err)
		assert.True(t, found)
		assert.True(t, bytes.Equal(entry.value, value))
	}
	require.NoError(t, db.Close())
	expected := []testEntry{record("", "empty key"), record("10", "ten"), record("2", ""), record("cat", "a longer replacement"), record("猫", "🐈"), record("\xff", "\x00\xff")}
	assertFileContents(t, path, databaseBytesWithEntries(expected))
	require.NoError(t, Validate(path))
	reopened, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, reopened.Close()) })
	for _, entry := range expected {
		value, found, err := reopened.Get(entry.key)
		require.NoError(t, err)
		assert.True(t, found)
		assert.True(t, bytes.Equal(entry.value, value))
	}
}

func TestDatabase_PutOwnsInputAndTreatsNilAndEmptyKeysEqually(t *testing.T) {
	path := writeDatabaseFixture(t, validDatabaseBytes())
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	key, value := []byte("cat"), []byte("animal")
	require.NoError(t, db.Put(key, value))
	key[0], value[0] = 'z', 'X'
	replacement := []byte("pet")
	require.NoError(t, db.Put([]byte("cat"), replacement))
	replacement[0] = 'X'
	require.NoError(t, db.Put(nil, []byte("first")))
	require.NoError(t, db.Put([]byte{}, nil))
	got, found, err := db.Get(nil)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Empty(t, got)
	assertFileContents(t, path, databaseBytesWithEntries([]testEntry{record("", ""), record("cat", "pet")}))
}

func TestDatabase_PutExactFitRejectsOversizedValueAndAllowsReplacement(t *testing.T) {
	path := writeDatabaseFixture(t, validDatabaseBytes())
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	value := bytes.Repeat([]byte{'v'}, 4088)
	require.NoError(t, db.Put([]byte("a"), value))
	full := databaseBytesWithEntries([]testEntry{{[]byte("a"), value}})
	assertFileContents(t, path, full)
	require.ErrorIs(t, db.Put([]byte("a"), make([]byte, 4089)), pager.ErrPagerEntryTooLarge)
	assertFileContents(t, path, full)
	replacement := bytes.Repeat([]byte{'w'}, 4088)
	require.NoError(t, db.Put([]byte("a"), replacement))
	require.NoError(t, db.Close())
	assertFileContents(t, path, databaseBytesWithEntries([]testEntry{{[]byte("a"), replacement}}))
	require.NoError(t, Validate(path))
	reopened, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, reopened.Close()) })
	got, found, err := reopened.Get([]byte("a"))
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, replacement, got)
}

func TestDatabase_PutGrowingValueCanFillPageAndShrinkingFreesSpace(t *testing.T) {
	entries := []testEntry{{[]byte("a"), make([]byte, 2000)}, {[]byte("b"), make([]byte, 2000)}}
	contents := databaseBytesWithEntries(entries)
	path := writeDatabaseFixture(t, contents)
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	require.NoError(t, db.Put([]byte("b"), make([]byte, 2083)))
	entries[1].value = make([]byte, 2083)
	assertFileContents(t, path, databaseBytesWithEntries(entries))
	require.NoError(t, db.Put([]byte("b"), nil))
	require.NoError(t, db.Put([]byte("c"), []byte("new")))
	entries[1].value = nil
	entries = append(entries, record("c", "new"))
	assertFileContents(t, path, databaseBytesWithEntries(entries))
}

func TestDatabase_PutOversizedKeyOrValuePreservesFile(t *testing.T) {
	contents := validDatabaseBytes()
	path := writeDatabaseFixture(t, contents)
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	for _, entry := range []testEntry{{make([]byte, 4090), nil}, {nil, make([]byte, 4090)}} {
		require.ErrorIs(t, db.Put(entry.key, entry.value), pager.ErrPagerEntryTooLarge)
		assertFileContents(t, path, contents)
	}
}

func TestDatabase_PutUsesHeaderRootPageIDAndPreservesOtherPages(t *testing.T) {
	contents := databaseBytesWithEntries([]testEntry{record("a", "old")})
	contents = append(contents, contents[4096:8192]...)
	binary.LittleEndian.PutUint64(contents[16:24], 2)
	binary.LittleEndian.PutUint64(contents[24:32], 3)
	path := writeDatabaseFixture(t, contents)
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	require.NoError(t, db.Put([]byte("a"), []byte("longer replacement")))
	require.NoError(t, db.Put([]byte("b"), nil))
	expected := bytes.Clone(contents)
	copy(expected[8192:], databaseBytesWithEntries([]testEntry{record("a", "longer replacement"), record("b", "")})[4096:])
	assertFileContents(t, path, expected)
	require.NoError(t, Validate(path))
}

func TestDatabase_PutAfterClosePreservesFile(t *testing.T) {
	contents := validDatabaseBytes()
	path := writeDatabaseFixture(t, contents)
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Close())
	require.ErrorIs(t, db.Put([]byte("a"), nil), os.ErrClosed)
	assertFileContents(t, path, contents)
}

func TestDatabase_PutWithTruncatedRootPreservesFile(t *testing.T) {
	contents := validDatabaseBytes()
	path := writeDatabaseFixture(t, contents)
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	require.NoError(t, os.Truncate(path, 4196))
	require.ErrorIs(t, db.Put([]byte("a"), nil), io.EOF)
	assertFileContents(t, path, contents[:4196])
}

func TestDatabase_PutWithCorruptedRootPreservesFile(t *testing.T) {
	contents := validDatabaseBytes()
	path := writeDatabaseFixture(t, contents)
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	contents[4096] = 255
	require.NoError(t, os.WriteFile(path, contents, 0600))
	require.ErrorIs(t, db.Put([]byte("a"), nil), pager.ErrPagerCorruptPage)
	assertFileContents(t, path, contents)
}
