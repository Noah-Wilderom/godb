package database

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"testing"

	"github.com/Noah-Wilderom/godb/pkg/pager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareTreeSplit_MaximumSupportedPageCount(t *testing.T) {
	const maximumPageCount = uint64(math.MaxInt64 / 4096)
	header := pager.NewDatabaseHeader()
	header.PageCount = maximumPageCount - 2
	before := *header
	db := &Database{header: header}
	leaf, _ := splitPlanFixture(t, []int{2000, 2084})
	plan, err := db.prepareTreeSplit(&leafLocation{pageID: header.RootPageID, leaf: leaf})
	require.NoError(t, err)
	assert.Equal(t, maximumPageCount, plan.header.PageCount)
	assert.Equal(t, maximumPageCount-1, plan.header.RootPageID)
	_, err = pager.EncodeHeader(&plan.header)
	require.NoError(t, err)
	root, err := pager.DecodeInternal(plan.appendedPages[len(plan.appendedPages)-1])
	require.NoError(t, err)
	assert.Equal(t, maximumPageCount-2, root.ChildFor([]byte("b")))
	assert.Equal(t, before, *db.header)
}

func splitPlanFixture(t testing.TB, sizes []int) (*pager.BPlusLeaf, []testEntry) {
	t.Helper()
	keys := make([][]byte, len(sizes))
	values := make([][]byte, len(sizes))
	entries := make([]testEntry, len(sizes))
	for i, size := range sizes {
		keys[i] = []byte{byte('a' + i)}
		values[i] = bytes.Repeat([]byte{byte(i + 1)}, size)
		entries[i] = testEntry{key: keys[i], value: values[i]}
	}
	leaf, err := pager.NewBPlusLeaf(keys, values)
	require.NoError(t, err)
	return leaf, entries
}

func TestPrepareLeafSplit_PagesAndSeparators(t *testing.T) {
	cases := []struct {
		name       string
		sizes      []int
		groups     []int
		separators []string
	}{
		{"Empty", nil, []int{0}, nil},
		{"AlreadyFits", []int{0, 2, 100}, []int{3}, nil},
		{"SingleExactFit", []int{4088}, []int{1}, nil},
		{"CombinedExactFit", []int{2000, 2083}, []int{2}, nil},
		{"OneByteOver", []int{2000, 2084}, []int{1, 1}, []string{"b"}},
		{"FullPageThenAnother", []int{4088, 0}, []int{1, 1}, []string{"b"}},
		{"TwoEntriesInRightPage", []int{3000, 2000, 2000}, []int{1, 2}, []string{"b"}},
		{"TwoEntriesInLeftPage", []int{1000, 1000, 3000}, []int{2, 1}, []string{"c"}},
		{"ThreePages", []int{1995, 2995, 1995}, []int{1, 1, 1}, []string{"b", "c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaf, entries := splitPlanFixture(t, tc.sizes)
			plan, err := prepareLeafSplit(leaf)
			require.NoError(t, err)
			require.NotNil(t, plan)
			require.Len(t, plan.pages, len(tc.groups))
			require.Len(t, plan.separators, len(tc.separators))
			for i, want := range tc.separators {
				assert.Equal(t, []byte(want), plan.separators[i])
			}
			start := 0
			for i, count := range tc.groups {
				expected := databaseBytesWithEntries(entries[start : start+count])[4096:]
				assert.Equal(t, expected, plan.pages[i])
				decoded, err := pager.DecodeLeaf(plan.pages[i])
				require.NoError(t, err)
				for j, entry := range entries {
					got, _, found := decoded.Get(entry.key)
					if j >= start && j < start+count {
						require.True(t, found)
						assert.True(t, bytes.Equal(entry.value, got))
					} else {
						assert.False(t, found)
					}
				}
				start += count
			}
			assert.Equal(t, len(entries), start)
			for _, entry := range entries {
				got, _, found := leaf.Get(entry.key)
				require.True(t, found)
				assert.True(t, bytes.Equal(entry.value, got))
			}
		})
	}
}

func TestPrepareLeafSplit_PreservesEmptyKeyAndValue(t *testing.T) {
	value := bytes.Repeat([]byte{'v'}, 4089)
	leaf, err := pager.NewBPlusLeaf([][]byte{nil, []byte("a")}, [][]byte{value, nil})
	require.NoError(t, err)
	plan, err := prepareLeafSplit(leaf)
	require.NoError(t, err)
	require.Len(t, plan.pages, 2)
	assert.Equal(t, [][]byte{[]byte("a")}, plan.separators)
	assert.Equal(t, databaseBytesWithEntries([]testEntry{{key: nil, value: value}})[4096:], plan.pages[0])
	assert.Equal(t, databaseBytesWithEntries([]testEntry{{key: []byte("a"), value: nil}})[4096:], plan.pages[1])
}

func TestPrepareLeafSplit_RejectsInvalidInputWithoutPartialPlan(t *testing.T) {
	t.Run("NilLeaf", func(t *testing.T) {
		plan, err := prepareLeafSplit(nil)
		require.Error(t, err)
		assert.Nil(t, plan)
	})
	cases := []struct {
		name       string
		key, value []byte
	}{
		{"OversizedKey", bytes.Repeat([]byte{'z'}, 4076), nil},
		{"OversizedValue", []byte("z"), make([]byte, 4089)},
		{"CombinedTooLarge", bytes.Repeat([]byte{'z'}, 4075), make([]byte, 15)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaf, _ := splitPlanFixture(t, []int{2500, 2500})
			leaf.Put(tc.key, tc.value)
			plan, err := prepareLeafSplit(leaf)
			require.ErrorIs(t, err, pager.ErrPagerEntryTooLarge)
			assert.Nil(t, plan)
		})
	}
}

func TestPrepareLeafSplit_OwnsPagesAndSeparators(t *testing.T) {
	leaf, _ := splitPlanFixture(t, []int{1995, 2995, 1995})
	plan, err := prepareLeafSplit(leaf)
	require.NoError(t, err)
	again, err := prepareLeafSplit(leaf)
	require.NoError(t, err)
	pageBefore := bytes.Clone(plan.pages[1])
	plan.separators[0][0] = 'z'
	assert.Equal(t, pageBefore, plan.pages[1])
	assert.Equal(t, []byte("b"), again.separators[0])
	plan.pages[0][0] = 0xff
	assert.Equal(t, byte(pager.PageTypeLeaf), plan.pages[1][0])
	assert.Equal(t, byte(pager.PageTypeLeaf), again.pages[0][0])
	leaf.Put([]byte("b"), []byte("changed"))
	assert.Equal(t, pageBefore, plan.pages[1])
	decoded, err := pager.DecodeLeaf(plan.pages[1])
	require.NoError(t, err)
	value, _, found := decoded.Get([]byte("b"))
	require.True(t, found)
	assert.Equal(t, bytes.Repeat([]byte{2}, 2995), value)
}

func TestPrepareTreeSplit_RootPositionsAndPageLayout(t *testing.T) {
	cases := []struct {
		name              string
		root, count       uint64
		sizes             []int
		groups            []int
		children          []uint64
		newRoot, newCount uint64
	}{
		{"TwoLeaves", 1, 2, []int{2000, 2084}, []int{1, 1}, []uint64{1, 2}, 3, 4},
		{"ThreeLeaves", 1, 2, []int{1995, 2995, 1995}, []int{1, 1, 1}, []uint64{1, 2, 3}, 4, 5},
		{"RootAwayFromPageOne", 3, 5, []int{3000, 2000, 2000}, []int{1, 2}, []uint64{3, 5}, 6, 7},
		{"SeparatorAfterTwoEntries", 2, 4, []int{1000, 1000, 3000}, []int{2, 1}, []uint64{2, 4}, 5, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pages := make([][]byte, int(tc.count)-1)
			for i := range pages {
				pages[i] = traversalLeafPage(t, "original")
			}
			db, path := newTraversalDatabase(t, tc.root, pages)
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			headerPointer := db.header
			headerBefore := *db.header
			leaf, entries := splitPlanFixture(t, tc.sizes)
			plan, err := db.prepareTreeSplit(&leafLocation{pageID: tc.root, leaf: leaf})
			require.NoError(t, err)
			require.NotNil(t, plan)
			require.Len(t, plan.updates, 1)
			require.Equal(t, tc.root, plan.updates[0].pageID)
			require.Len(t, plan.appendedPages, len(tc.groups))
			leafPages := append([][]byte{plan.updates[0].page}, plan.appendedPages[:len(plan.appendedPages)-1]...)
			rootPage := plan.appendedPages[len(plan.appendedPages)-1]
			headerPage, err := pager.EncodeHeader(&plan.header)
			require.NoError(t, err)
			require.Len(t, rootPage, 4096)
			require.Len(t, headerPage, 4096)
			assert.Same(t, headerPointer, db.header)
			assert.Equal(t, headerBefore, *db.header)
			assertFileContents(t, path, before)

			expectedHeader := headerBefore
			expectedHeader.RootPageID = tc.newRoot
			expectedHeader.PageCount = tc.newCount
			assert.Equal(t, expectedHeader, plan.header)
			decodedHeader, err := pager.DecodeHeader(headerPage)
			require.NoError(t, err)
			assert.Equal(t, expectedHeader, *decodedHeader)
			assert.Equal(t, tc.newRoot, binary.LittleEndian.Uint64(headerPage[16:24]))
			assert.Equal(t, tc.newCount, binary.LittleEndian.Uint64(headerPage[24:32]))

			expectedRoot := make([]byte, 4096)
			expectedRoot[0] = 2
			binary.LittleEndian.PutUint16(expectedRoot[1:3], uint16(len(tc.groups)-1))
			binary.LittleEndian.PutUint64(expectedRoot[3:11], tc.children[0])
			start, offset := 0, 11
			for i, count := range tc.groups {
				assert.Equal(t, databaseBytesWithEntries(entries[start : start+count])[4096:], leafPages[i])
				if i > 0 {
					copy(expectedRoot[offset:offset+3], []byte{1, 0, entries[start].key[0]})
					binary.LittleEndian.PutUint64(expectedRoot[offset+3:offset+11], tc.children[i])
					offset += 11
				}
				start += count
			}
			assert.Equal(t, expectedRoot, rootPage)
			root, err := pager.DecodeInternal(rootPage)
			require.NoError(t, err)
			start = 0
			for i, count := range tc.groups {
				for _, entry := range entries[start : start+count] {
					assert.Equal(t, tc.children[i], root.ChildFor(entry.key))
					value, _, found := leaf.Get(entry.key)
					require.True(t, found)
					assert.True(t, bytes.Equal(entry.value, value))
				}
				start += count
			}
			plan.header.RootPageID = 0
			assert.Equal(t, headerBefore, *db.header)
			assertFileContents(t, path, before)
		})
	}
}
