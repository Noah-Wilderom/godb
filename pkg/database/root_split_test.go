package database

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/pager"
)

func rootSplitEntry(key string, valueSize int) testEntry {
	return testEntry{key: []byte(key), value: bytes.Repeat([]byte{'v'}, valueSize)}
}

func TestDatabase_PutAutomaticallySplitsRootLeaf(t *testing.T) {
	cases := []struct {
		name    string
		initial []testEntry
		update  testEntry
		groups  [][]testEntry
		root    uint64
	}{
		{"InsertRight", []testEntry{rootSplitEntry("a", 2000)}, rootSplitEntry("b", 2084), [][]testEntry{{rootSplitEntry("a", 2000)}, {rootSplitEntry("b", 2084)}}, 1},
		{"InsertLeft", []testEntry{rootSplitEntry("b", 2084)}, rootSplitEntry("a", 2000), [][]testEntry{{rootSplitEntry("a", 2000)}, {rootSplitEntry("b", 2084)}}, 1},
		{"GrowFirstValue", []testEntry{rootSplitEntry("a", 2000), rootSplitEntry("b", 2000)}, rootSplitEntry("a", 2090), [][]testEntry{{rootSplitEntry("a", 2090)}, {rootSplitEntry("b", 2000)}}, 1},
		{"GrowLastValue", []testEntry{rootSplitEntry("a", 2000), rootSplitEntry("b", 2000)}, rootSplitEntry("b", 2090), [][]testEntry{{rootSplitEntry("a", 2000)}, {rootSplitEntry("b", 2090)}}, 1},
		{"ThreeLeaves", []testEntry{rootSplitEntry("a", 1995), rootSplitEntry("c", 1995)}, rootSplitEntry("b", 2995), [][]testEntry{{rootSplitEntry("a", 1995)}, {rootSplitEntry("b", 2995)}, {rootSplitEntry("c", 1995)}}, 1},
		{"EmptyKey", []testEntry{rootSplitEntry("a", 4088)}, testEntry{key: nil, value: nil}, [][]testEntry{{record("", "")}, {rootSplitEntry("a", 4088)}}, 1},
		{"RelocatedRoot", []testEntry{rootSplitEntry("a", 2000)}, rootSplitEntry("b", 2084), [][]testEntry{{rootSplitEntry("a", 2000)}, {rootSplitEntry("b", 2084)}}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initial := databaseBytesWithEntries(tc.initial)
			if tc.root == 2 {
				initial = append(initial, initial[4096:8192]...)
				binary.LittleEndian.PutUint64(initial[16:24], 2)
				binary.LittleEndian.PutUint64(initial[24:32], 3)
			}

			oldCount := uint64(len(initial) / 4096)
			path := writeDatabaseFixture(t, initial)
			db, err := Open(path)
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			require.NoError(t, db.Put(tc.update.key, tc.update.value))

			children := make([]uint64, len(tc.groups))
			children[0] = tc.root
			expected := bytes.Clone(initial)
			copy(expected[int(tc.root)*4096:], databaseBytesWithEntries(tc.groups[0])[4096:])
			rootPage := make([]byte, 4096)
			rootPage[0] = 2
			binary.LittleEndian.PutUint16(rootPage[1:3], uint16(len(tc.groups)-1))
			binary.LittleEndian.PutUint64(rootPage[3:11], tc.root)
			offset := 11

			for i := 1; i < len(tc.groups); i++ {
				children[i] = oldCount + uint64(i-1)
				expected = append(expected, databaseBytesWithEntries(tc.groups[i])[4096:]...)
				key := tc.groups[i][0].key
				binary.LittleEndian.PutUint16(rootPage[offset:offset+2], uint16(len(key)))
				copy(rootPage[offset+2:], key)
				offset += 2 + len(key)
				binary.LittleEndian.PutUint64(rootPage[offset:offset+8], children[i])
				offset += 8
			}

			expected = append(expected, rootPage...)
			newCount := oldCount + uint64(len(tc.groups))
			binary.LittleEndian.PutUint64(expected[16:24], newCount-1)
			binary.LittleEndian.PutUint64(expected[24:32], newCount)
			assertFileContents(t, path, expected)
			assert.Equal(t, newCount-1, db.header.RootPageID)
			assert.Equal(t, newCount, db.header.PageCount)

			for i, group := range tc.groups {
				for _, entry := range group {
					location, err := db.findLeaf(entry.key)
					require.NoError(t, err)
					require.NotNil(t, location)
					assert.Equal(t, children[i], location.pageID)
					assert.Equal(t, []uint64{newCount - 1}, location.parents)
					got, found, err := db.Get(entry.key)
					require.NoError(t, err)
					require.True(t, found)
					assert.True(t, bytes.Equal(entry.value, got))
				}
			}

			require.NoError(t, db.Close())
			require.NoError(t, Validate(path))
			reopened, err := Open(path)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, reopened.Close()) })

			for _, group := range tc.groups {
				for _, entry := range group {
					got, found, err := reopened.Get(entry.key)
					require.NoError(t, err)
					require.True(t, found)
					assert.True(t, bytes.Equal(entry.value, got))
				}
			}

			got, found, err := reopened.Get([]byte("missing"))
			require.NoError(t, err)
			assert.False(t, found)
			assert.Nil(t, got)
			require.NoError(t, reopened.Put(tc.groups[0][0].key, []byte("replacement")))
			got, found, err = reopened.Get(tc.groups[0][0].key)
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, []byte("replacement"), got)
		})
	}
}

func TestDatabase_RootSplitRejectsMismatchedFileSizeBeforeWriting(t *testing.T) {
	for _, extra := range []int{1, 4096} {
		t.Run(fmt.Sprintf("ExtraBytes%d", extra), func(t *testing.T) {
			initial := databaseBytesWithEntries([]testEntry{rootSplitEntry("a", 2000)})
			path := writeDatabaseFixture(t, initial)
			db, err := Open(path)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, db.Close()) })
			require.NoError(t, os.Truncate(path, int64(len(initial)+extra)))
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			headerBefore := *db.header
			require.ErrorIs(t, db.Put([]byte("b"), make([]byte, 2084)), pager.ErrPagerCorruptPage)
			assertFileContents(t, path, before)
			assert.Equal(t, headerBefore, *db.header)
		})
	}
}

func TestDatabase_RootSplitHandlesOversizedParent(t *testing.T) {
	initial := databaseBytesWithEntries([]testEntry{rootSplitEntry("a", 995), {key: bytes.Repeat([]byte{'c'}, 100)}})
	path := writeDatabaseFixture(t, initial)
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	require.NoError(t, db.Put(bytes.Repeat([]byte{'b'}, 4075), nil))
	assert.Equal(t, uint64(6), db.header.RootPageID)
	assert.Equal(t, uint64(7), db.header.PageCount)

	for _, key := range [][]byte{[]byte("a"), bytes.Repeat([]byte{'b'}, 4075), bytes.Repeat([]byte{'c'}, 100)} {
		_, found, err := db.Get(key)
		require.NoError(t, err)
		require.True(t, found)
	}

	require.NoError(t, Validate(path))
}

func TestDatabase_RootSplitCanBeFollowedByNonRootSplit(t *testing.T) {
	path := writeDatabaseFixture(t, databaseBytesWithEntries([]testEntry{rootSplitEntry("a", 4088)}))
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	require.NoError(t, db.Put([]byte("b"), nil))
	headerBefore := *db.header
	require.NoError(t, db.Put(nil, nil))
	assert.Equal(t, headerBefore.RootPageID, db.header.RootPageID)
	assert.Equal(t, headerBefore.PageCount+1, db.header.PageCount)
	value, found, err := db.Get(nil)
	require.NoError(t, err)
	require.True(t, found)
	assert.Empty(t, value)
	got, found, err := db.Get([]byte("a"))
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, bytes.Repeat([]byte{'v'}, 4088), got)
	_, found, err = db.Get([]byte("b"))
	require.NoError(t, err)
	require.True(t, found)
	require.NoError(t, Validate(path))
}
