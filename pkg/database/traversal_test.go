package database

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/Noah-Wilderom/godb/pkg/pager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func traversalLeafPage(t testing.TB, keys ...string) []byte {
	t.Helper()
	keyBytes := make([][]byte, len(keys))
	values := make([][]byte, len(keys))
	for i, key := range keys {
		keyBytes[i] = []byte(key)
		values[i] = []byte("value:" + key)
	}
	leaf, err := pager.NewBPlusLeaf(keyBytes, values)
	require.NoError(t, err)
	page, err := pager.EncodeLeaf(leaf)
	require.NoError(t, err)
	return page
}

func traversalInternalPage(t testing.TB, keys []string, children []uint64) []byte {
	t.Helper()
	keyBytes := make([][]byte, len(keys))
	for i, key := range keys {
		keyBytes[i] = []byte(key)
	}
	node, err := pager.NewBPlusInternal(keyBytes, children)
	require.NoError(t, err)
	page, err := pager.EncodeInternal(node)
	require.NoError(t, err)
	return page
}

func traversalPages(t testing.TB) [][]byte {
	t.Helper()
	return [][]byte{
		traversalLeafPage(t, "ant", "bee"),
		traversalLeafPage(t, "cat", "catalog"),
		traversalLeafPage(t, "dog", "fox"),
		traversalLeafPage(t, "yak", "zebra"),
		traversalInternalPage(t, []string{"cat"}, []uint64{1, 2}),
		traversalInternalPage(t, []string{"yak"}, []uint64{3, 4}),
		traversalInternalPage(t, []string{"dog"}, []uint64{5, 6}),
	}
}

func newTraversalDatabase(t testing.TB, root uint64, pages [][]byte) (*Database, string) {
	t.Helper()
	header := pager.NewDatabaseHeader()
	header.RootPageID = root
	header.PageCount = uint64(len(pages) + 1)
	contents, err := pager.EncodeHeader(header)
	require.NoError(t, err)
	for _, page := range pages {
		require.Len(t, page, 4096)
		contents = append(contents, page...)
	}
	path := writeDatabaseFixture(t, contents)
	p, err := pager.OpenPager(path)
	require.NoError(t, err)
	db := &Database{pager: p, header: header}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

func TestDatabase_OpenAndGetThroughInternalRoots(t *testing.T) {
	cases := []struct {
		name    string
		root    uint64
		pages   [][]byte
		present []string
		missing []string
	}{
		{"SingleChildRoot", 2, [][]byte{traversalLeafPage(t, "cat"), traversalInternalPage(t, nil, []uint64{1})}, []string{"cat"}, []string{"", "ant", "dog"}},
		{"ThreeLevels", 7, traversalPages(t), []string{"ant", "bee", "cat", "catalog", "dog", "fox", "yak", "zebra"}, []string{"", "car", "cow", "wolf", "zz"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture, path := newTraversalDatabase(t, tc.root, tc.pages)
			require.NoError(t, fixture.Close())
			original, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NoError(t, Validate(path))
			for pass := 0; pass < 2; pass++ {
				db, err := Open(path)
				require.NoError(t, err)
				t.Cleanup(func() { _ = db.Close() })
				for _, key := range tc.present {
					value, found, err := db.Get([]byte(key))
					require.NoError(t, err)
					require.True(t, found, "key %q", key)
					assert.Equal(t, []byte("value:"+key), value)
				}
				for _, key := range tc.missing {
					value, found, err := db.Get([]byte(key))
					require.NoError(t, err)
					assert.False(t, found, "key %q", key)
					assert.Nil(t, value)
				}
				value, found, err := db.Get(nil)
				require.NoError(t, err)
				assert.False(t, found)
				assert.Nil(t, value)
				require.NoError(t, db.Close())
			}
			assertFileContents(t, path, original)
		})
	}
}

func TestOpenAndValidate_RejectMalformedInternalRoot(t *testing.T) {
	cases := []struct {
		name   string
		mutate func([]byte)
	}{
		{"TooManyEntries", func(p []byte) { binary.LittleEndian.PutUint16(p[1:3], 65535) }},
		{"OversizedKeyLength", func(p []byte) { binary.LittleEndian.PutUint16(p[11:13], 65535) }},
		{"ZeroChild", func(p []byte) { binary.LittleEndian.PutUint64(p[3:11], 0) }},
		{"DuplicateChildren", func(p []byte) { binary.LittleEndian.PutUint64(p[16:24], 5) }},
		{"UnknownType", func(p []byte) { p[0] = 0xff }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pages := traversalPages(t)
			tc.mutate(pages[6])
			fixture, path := newTraversalDatabase(t, 7, pages)
			require.NoError(t, fixture.Close())
			require.ErrorIs(t, Validate(path), pager.ErrPagerCorruptPage)
			db, err := Open(path)
			if db != nil {
				t.Cleanup(func() { _ = db.Close() })
			}
			require.ErrorIs(t, err, pager.ErrPagerCorruptPage)
			assert.Nil(t, db)
		})
	}
}

func TestDatabase_GetPropagatesTraversalCorruption(t *testing.T) {
	cases := []struct {
		name   string
		pageID uint64
		mutate func([]byte)
	}{
		{"Cycle", 5, func(p []byte) { binary.LittleEndian.PutUint64(p[3:11], 7) }},
		{"ChildOutsideDatabase", 5, func(p []byte) { binary.LittleEndian.PutUint64(p[3:11], 8) }},
		{"UnknownChildType", 5, func(p []byte) { p[0] = 0xff }},
		{"MalformedLeaf", 1, func(p []byte) { binary.LittleEndian.PutUint16(p[1:3], 65535) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pages := traversalPages(t)
			fixture, path := newTraversalDatabase(t, 7, pages)
			require.NoError(t, fixture.Close())
			db, err := Open(path)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, db.Close()) })
			page := pages[tc.pageID-1]
			tc.mutate(page)
			require.NoError(t, db.pager.WritePage(tc.pageID, page))
			value, found, err := db.Get([]byte("ant"))
			require.ErrorIs(t, err, pager.ErrPagerCorruptPage)
			assert.False(t, found)
			assert.Nil(t, value)
		})
	}
}

func TestDatabase_FindLeafTraversesMultipleLevels(t *testing.T) {
	db, path := newTraversalDatabase(t, 7, traversalPages(t))
	original, err := os.ReadFile(path)
	require.NoError(t, err)
	cases := []struct {
		key    string
		pageID uint64
		found  bool
	}{
		{"", 1, false}, {"ant", 1, true}, {"bee", 1, true}, {"car", 1, false},
		{"cat", 2, true}, {"catalog", 2, true}, {"cow", 2, false},
		{"dog", 3, true}, {"fox", 3, true}, {"wolf", 3, false},
		{"yak", 4, true}, {"zebra", 4, true}, {"zz", 4, false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("Key_%q", tc.key), func(t *testing.T) {
			location, err := db.findLeaf([]byte(tc.key))
			require.NoError(t, err)
			require.NotNil(t, location)
			require.NotNil(t, location.leaf)
			assert.Equal(t, tc.pageID, location.pageID)
			if tc.pageID <= 2 {
				assert.Equal(t, []uint64{7, 5}, location.parents)
			} else {
				assert.Equal(t, []uint64{7, 6}, location.parents)
			}
			value, _, found := location.leaf.Get([]byte(tc.key))
			assert.Equal(t, tc.found, found)
			if tc.found {
				assert.Equal(t, []byte("value:"+tc.key), value)
			} else {
				assert.Nil(t, value)
			}
		})
	}
	location, err := db.findLeaf(nil)
	require.NoError(t, err)
	require.NotNil(t, location)
	assert.Equal(t, uint64(1), location.pageID)
	assert.Equal(t, []uint64{7, 5}, location.parents)
	assertFileContents(t, path, original)
}

func TestDatabase_FindLeafHandlesLeafRootsAndSingleChildNodes(t *testing.T) {
	cases := []struct {
		name  string
		root  uint64
		pages [][]byte
		key   string
		found bool
	}{
		{"LeafRoot", 1, [][]byte{traversalLeafPage(t, "cat")}, "cat", true},
		{"EmptyLeafRoot", 1, [][]byte{traversalLeafPage(t)}, "cat", false},
		{"EmptyKey", 1, [][]byte{traversalLeafPage(t, "")}, "", true},
		{"SingleChild", 2, [][]byte{traversalLeafPage(t, "cat"), traversalInternalPage(t, nil, []uint64{1})}, "cat", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := newTraversalDatabase(t, tc.root, tc.pages)
			location, err := db.findLeaf([]byte(tc.key))
			require.NoError(t, err)
			require.NotNil(t, location)
			require.NotNil(t, location.leaf)
			assert.Equal(t, uint64(1), location.pageID)
			if tc.root == 1 {
				assert.Empty(t, location.parents)
			} else {
				assert.Equal(t, []uint64{2}, location.parents)
			}
			_, _, found := location.leaf.Get([]byte(tc.key))
			assert.Equal(t, tc.found, found)
		})
	}
}

func TestDatabase_FindLeafRejectsCorruptPaths(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func([][]byte)
		message string
	}{
		{"SelfCycle", func(p [][]byte) { binary.LittleEndian.PutUint64(p[6][3:11], 7) }, "cycle detected"},
		{"TwoPageCycle", func(p [][]byte) { binary.LittleEndian.PutUint64(p[4][3:11], 7) }, "cycle detected"},
		{"ChildAtPageCount", func(p [][]byte) { binary.LittleEndian.PutUint64(p[6][3:11], 8) }, "invalid page ID"},
		{"ChildAbovePageCount", func(p [][]byte) { binary.LittleEndian.PutUint64(p[4][3:11], 9) }, "invalid page ID"},
		{"ChildPageZero", func(p [][]byte) { binary.LittleEndian.PutUint64(p[6][3:11], 0) }, "decode internal"},
		{"DuplicateChildren", func(p [][]byte) { binary.LittleEndian.PutUint64(p[6][16:24], 5) }, "decode internal"},
		{"UnknownRootType", func(p [][]byte) { p[6][0] = 0xff }, "unknown page type"},
		{"UnknownChildType", func(p [][]byte) { p[4][0] = 0xff }, "unknown page type"},
		{"CorruptInternal", func(p [][]byte) { binary.LittleEndian.PutUint16(p[4][1:3], 65535) }, "decode internal"},
		{"CorruptLeaf", func(p [][]byte) { binary.LittleEndian.PutUint16(p[0][1:3], 65535) }, "decode leaf"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pages := traversalPages(t)
			tc.mutate(pages)
			db, _ := newTraversalDatabase(t, 7, pages)
			location, err := db.findLeaf([]byte("ant"))
			require.ErrorIs(t, err, pager.ErrPagerCorruptPage)
			assert.Contains(t, err.Error(), tc.message)
			assert.Nil(t, location)
		})
	}
}

func TestDatabase_FindLeafRejectsInvalidRootID(t *testing.T) {
	for _, root := range []uint64{0, 8, 9, ^uint64(0)} {
		t.Run(fmt.Sprintf("Root%d", root), func(t *testing.T) {
			db, _ := newTraversalDatabase(t, 7, traversalPages(t))
			db.header.RootPageID = root
			location, err := db.findLeaf(nil)
			require.ErrorIs(t, err, pager.ErrPagerCorruptPage)
			assert.Nil(t, location)
		})
	}
}

func TestDatabase_FindLeafPropagatesReadErrors(t *testing.T) {
	for _, size := range []int64{4096, 4096 + 100} {
		t.Run(fmt.Sprintf("FileBytes%d", size), func(t *testing.T) {
			pages := [][]byte{traversalInternalPage(t, []string{"cat"}, []uint64{2, 3}), traversalLeafPage(t, "ant"), traversalLeafPage(t, "cat")}
			db, path := newTraversalDatabase(t, 1, pages)
			require.NoError(t, os.Truncate(path, size))
			location, err := db.findLeaf([]byte("ant"))
			require.ErrorIs(t, err, io.EOF)
			assert.Nil(t, location)
		})
	}
	t.Run("MissingChild", func(t *testing.T) {
		pages := [][]byte{traversalInternalPage(t, []string{"cat"}, []uint64{2, 3}), traversalLeafPage(t, "ant"), traversalLeafPage(t, "cat")}
		db, path := newTraversalDatabase(t, 1, pages)
		require.NoError(t, os.Truncate(path, 8192))
		location, err := db.findLeaf([]byte("ant"))
		require.ErrorIs(t, err, io.EOF)
		assert.Contains(t, err.Error(), "read page 2")
		assert.Nil(t, location)
	})
	t.Run("ClosedFile", func(t *testing.T) {
		db, _ := newTraversalDatabase(t, 7, traversalPages(t))
		require.NoError(t, db.Close())
		location, err := db.findLeaf(nil)
		require.ErrorIs(t, err, os.ErrClosed)
		assert.Nil(t, location)
	})
}

func TestDatabase_FindLeafReturnsIndependentParentPaths(t *testing.T) {
	db, _ := newTraversalDatabase(t, 7, traversalPages(t))
	first, err := db.findLeaf([]byte("cat"))
	require.NoError(t, err)
	second, err := db.findLeaf([]byte("yak"))
	require.NoError(t, err)
	require.Equal(t, []uint64{7, 5}, first.parents)
	require.Equal(t, []uint64{7, 6}, second.parents)
	first.parents[0] = 99
	assert.Equal(t, []uint64{7, 6}, second.parents)
	again, err := db.findLeaf([]byte("cat"))
	require.NoError(t, err)
	assert.Equal(t, []uint64{7, 5}, again.parents)
	assert.Equal(t, uint64(2), again.pageID)
}

func TestDatabase_FindLeafRecordsParentsInTraversalOrder(t *testing.T) {
	pages := [][]byte{
		traversalInternalPage(t, nil, []uint64{3}),
		traversalInternalPage(t, nil, []uint64{4}),
		traversalInternalPage(t, nil, []uint64{2}),
		traversalLeafPage(t, "cat"),
	}
	db, _ := newTraversalDatabase(t, 1, pages)
	location, err := db.findLeaf([]byte("cat"))
	require.NoError(t, err)
	assert.Equal(t, uint64(4), location.pageID)
	assert.Equal(t, []uint64{1, 3, 2}, location.parents)
}
