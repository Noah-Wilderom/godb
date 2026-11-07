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

func TestDatabase_PutSplitsNonRootLeafAndPreservesOtherBranches(t *testing.T) {
	cases := []struct {
		name             string
		leafID, parentID uint64
		initial          []testEntry
		update           testEntry
		groups           [][]testEntry
		parentKeys       []string
		children         []uint64
	}{
		{"FirstLeaf", 1, 5, nil, rootSplitEntry("ant", 4086), [][]testEntry{{rootSplitEntry("ant", 4086)}, {record("bee", "value:bee")}}, []string{"bee", "cat"}, []uint64{1, 8, 2}},
		{"ReplaceSeparator", 2, 5, nil, rootSplitEntry("cat", 4086), [][]testEntry{{rootSplitEntry("cat", 4086)}, {record("catalog", "value:catalog")}}, []string{"cat", "catalog"}, []uint64{1, 2, 8}},
		{"InsertIntoSecondLeaf", 2, 5, nil, rootSplitEntry("cow", 4086), [][]testEntry{{record("cat", "value:cat"), record("catalog", "value:catalog")}, {rootSplitEntry("cow", 4086)}}, []string{"cat", "cow"}, []uint64{1, 2, 8}},
		{"RightBranch", 3, 6, nil, rootSplitEntry("dog", 4086), [][]testEntry{{rootSplitEntry("dog", 4086)}, {record("fox", "value:fox")}}, []string{"fox", "yak"}, []uint64{3, 8, 4}},
		{"LastLeaf", 4, 6, nil, rootSplitEntry("yak", 4086), [][]testEntry{{rootSplitEntry("yak", 4086)}, {record("zebra", "value:zebra")}}, []string{"yak", "zebra"}, []uint64{3, 4, 8}},
		{"ThreeLeaves", 1, 5, []testEntry{rootSplitEntry("ant", 1993), rootSplitEntry("bee", 1993)}, rootSplitEntry("bat", 2993), [][]testEntry{{rootSplitEntry("ant", 1993)}, {rootSplitEntry("bat", 2993)}, {rootSplitEntry("bee", 1993)}}, []string{"bat", "bee", "cat"}, []uint64{1, 8, 9, 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pages := traversalPages(t)
			if tc.initial != nil {
				pages[tc.leafID-1] = databaseBytesWithEntries(tc.initial)[4096:]
			}

			fixture, path := newTraversalDatabase(t, 7, pages)
			require.NoError(t, fixture.Close())
			db, err := Open(path)
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NoError(t, db.Put(tc.update.key, tc.update.value))
			expected := bytes.Clone(before)
			copy(expected[int(tc.leafID)*4096:], databaseBytesWithEntries(tc.groups[0])[4096:])
			copy(expected[int(tc.parentID)*4096:], traversalInternalPage(t, tc.parentKeys, tc.children))

			for _, group := range tc.groups[1:] {
				expected = append(expected, databaseBytesWithEntries(group)[4096:]...)
			}

			newCount := uint64(8 + len(tc.groups) - 1)
			binary.LittleEndian.PutUint64(expected[24:32], newCount)
			assertFileContents(t, path, expected)
			assert.Equal(t, uint64(7), db.header.RootPageID)
			assert.Equal(t, newCount, db.header.PageCount)

			for i, group := range tc.groups {
				wantID := tc.leafID
				if i > 0 {
					wantID = uint64(8 + i - 1)
				}

				for _, entry := range group {
					location, err := db.findLeaf(entry.key)
					require.NoError(t, err)
					assert.Equal(t, wantID, location.pageID)
					assert.Equal(t, []uint64{7, tc.parentID}, location.parents)
					value, found, err := db.Get(entry.key)
					require.NoError(t, err)
					require.True(t, found)
					assert.Equal(t, entry.value, value)
				}
			}

			require.NoError(t, db.Close())
			require.NoError(t, Validate(path))
			reopened, err := Open(path)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, reopened.Close()) })

			for _, key := range []string{"ant", "bee", "cat", "catalog", "dog", "fox", "yak", "zebra", string(tc.update.key)} {
				want := []byte("value:" + key)
				for _, entry := range tc.initial {
					if bytes.Equal(entry.key, []byte(key)) {
						want = entry.value
					}
				}

				if bytes.Equal(tc.update.key, []byte(key)) {
					want = tc.update.value
				}

				value, found, err := reopened.Get([]byte(key))
				require.NoError(t, err)
				require.True(t, found, "key %q", key)
				assert.Equal(t, want, value)
			}
		})
	}
}

func TestDatabase_PutSupportsRepeatedLeafSplits(t *testing.T) {
	for _, order := range []string{"Ascending", "Descending", "Mixed"} {
		t.Run(order, func(t *testing.T) {
			path := writeDatabaseFixture(t, validDatabaseBytes())
			db, err := Open(path)
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })

			for i := 0; i < 80; i++ {
				index := i

				switch order {
				case "Descending":
					index = 79 - i
				case "Mixed":
					index = (i * 37) % 80
				}

				require.NoError(t, db.Put([]byte(fmt.Sprintf("%04d", index)), bytes.Repeat([]byte{byte(index)}, 1000)))

				if i >= 4 {
					assert.Equal(t, uint64(3), db.header.RootPageID)
				}
			}

			assert.Greater(t, db.header.PageCount, uint64(4))
			require.NoError(t, db.Close())
			require.NoError(t, Validate(path))
			reopened, err := Open(path)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, reopened.Close()) })

			for i := 0; i < 80; i++ {
				value, found, err := reopened.Get([]byte(fmt.Sprintf("%04d", i)))
				require.NoError(t, err)
				require.True(t, found)
				assert.Equal(t, bytes.Repeat([]byte{byte(i)}, 1000), value)
			}
		})
	}
}

func TestDatabase_NonRootSplitHandlesFullParent(t *testing.T) {
	pages := traversalPages(t)
	separator := string(bytes.Repeat([]byte{'c'}, 3000))
	pages[0] = databaseBytesWithEntries([]testEntry{rootSplitEntry("a", 3000)})[4096:]
	pages[1] = databaseBytesWithEntries([]testEntry{record(separator, "")})[4096:]
	pages[4] = traversalInternalPage(t, []string{separator}, []uint64{1, 2})
	fixture, path := newTraversalDatabase(t, 7, pages)
	require.NoError(t, fixture.Close())
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	require.NoError(t, db.Put(bytes.Repeat([]byte{'b'}, 2000), nil))
	assert.Equal(t, uint64(7), db.header.RootPageID)
	assert.Equal(t, uint64(10), db.header.PageCount)

	for _, key := range [][]byte{[]byte("a"), bytes.Repeat([]byte{'b'}, 2000), []byte(separator), []byte("dog"), []byte("fox"), []byte("yak"), []byte("zebra")} {
		_, found, err := db.Get(key)
		require.NoError(t, err)
		require.True(t, found)
	}

	require.NoError(t, Validate(path))
}

func TestDatabase_NonRootSplitRejectsFileSizeMismatchBeforeWriting(t *testing.T) {
	for _, extra := range []int64{1, 4096} {
		t.Run(fmt.Sprintf("ExtraBytes%d", extra), func(t *testing.T) {
			db, path := newTraversalDatabase(t, 7, traversalPages(t))
			require.NoError(t, os.Truncate(path, 8*4096+extra))
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			headerBefore := *db.header
			require.ErrorIs(t, db.Put([]byte("cat"), make([]byte, 4086)), pager.ErrPagerCorruptPage)
			assert.Equal(t, headerBefore, *db.header)
			assertFileContents(t, path, before)
		})
	}
}
