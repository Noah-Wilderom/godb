package database

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/Noah-Wilderom/godb/pkg/pager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type treeSplitFixture struct {
	db       *Database
	path     string
	location *leafLocation
	want     *treeSplitPlan
	entries  []testEntry
	insert   testEntry
}

func treeSplitCases() []string {
	return []string{"RootLeaf", "ThreeLeafPieces", "ImmediateParent", "InternalRoot", "GrandparentAbsorbs", "CascadeToNewRoot", "OversizedNewRoot"}
}

func newTreeSplitFixture(t testing.TB, name string) treeSplitFixture {
	t.Helper()
	leafPage := func(entries ...testEntry) []byte {
		return databaseBytesWithEntries(entries)[4096:]
	}
	var pages [][]byte
	var root, newRoot, newCount uint64
	var updates []pageUpdate
	var appended [][]byte
	var entries []testEntry
	var insert testEntry
	a := rootSplitEntry("a", 3000)
	b := record(string(bytes.Repeat([]byte{'b'}, 2000)), "")
	c := record(string(bytes.Repeat([]byte{'c'}, 3000)), "")
	switch name {
	case "RootLeaf":
		a = rootSplitEntry("a", 2000)
		insert = rootSplitEntry("b", 2084)
		root, newRoot, newCount = 1, 3, 4
		pages = [][]byte{leafPage(a)}
		entries = []testEntry{a, insert}
		updates = []pageUpdate{{1, leafPage(a)}}
		appended = [][]byte{leafPage(insert), traversalInternalPage(t, []string{"b"}, []uint64{1, 2})}
	case "ThreeLeafPieces":
		a, c = rootSplitEntry("a", 1995), rootSplitEntry("c", 1995)
		insert = rootSplitEntry("b", 2995)
		root, newRoot, newCount = 1, 4, 5
		pages = [][]byte{leafPage(a, c)}
		entries = []testEntry{a, insert, c}
		updates = []pageUpdate{{1, leafPage(a)}}
		appended = [][]byte{leafPage(insert), leafPage(c), traversalInternalPage(t, []string{"b", "c"}, []uint64{1, 2, 3})}
	case "ImmediateParent":
		root, newRoot, newCount = 7, 7, 9
		pages = traversalPages(t)
		insert = rootSplitEntry("cat", 4086)
		for _, key := range []string{"ant", "bee", "cat", "catalog", "dog", "fox", "yak", "zebra"} {
			entry := record(key, "value:"+key)
			if key == "cat" {
				entry = insert
			}
			entries = append(entries, entry)
		}
		updates = []pageUpdate{
			{2, leafPage(insert)},
			{5, traversalInternalPage(t, []string{"cat", "catalog"}, []uint64{1, 2, 8})},
		}
		appended = [][]byte{traversalLeafPage(t, "catalog")}
	case "InternalRoot":
		insert = b
		root, newRoot, newCount = 3, 6, 7
		pages = [][]byte{leafPage(a), leafPage(c), traversalInternalPage(t, []string{string(c.key)}, []uint64{1, 2})}
		entries = []testEntry{a, b, c}
		updates = []pageUpdate{
			{1, leafPage(a)},
			{3, traversalInternalPage(t, []string{string(b.key)}, []uint64{1, 4})},
		}
		appended = [][]byte{
			leafPage(b),
			traversalInternalPage(t, nil, []uint64{2}),
			traversalInternalPage(t, []string{string(c.key)}, []uint64{3, 5}),
		}
	case "GrandparentAbsorbs", "CascadeToNewRoot":
		e := record("e", "")
		if name == "CascadeToNewRoot" {
			e = record(string(bytes.Repeat([]byte{'e'}, 3000)), "")
		}
		insert = b
		root, newRoot, newCount = 6, 6, 9
		pages = [][]byte{
			leafPage(a), leafPage(c), leafPage(e),
			traversalInternalPage(t, []string{string(c.key)}, []uint64{1, 2}),
			traversalInternalPage(t, nil, []uint64{3}),
			traversalInternalPage(t, []string{string(e.key)}, []uint64{4, 5}),
		}
		entries = []testEntry{a, b, c, e}
		updates = []pageUpdate{{1, leafPage(a)}, {4, traversalInternalPage(t, []string{string(b.key)}, []uint64{1, 7})}}
		appended = [][]byte{leafPage(b), traversalInternalPage(t, nil, []uint64{2})}
		if name == "GrandparentAbsorbs" {
			updates = append(updates, pageUpdate{6, traversalInternalPage(t, []string{string(c.key), string(e.key)}, []uint64{4, 8, 5})})
		} else {
			newRoot, newCount = 10, 11
			updates = append(updates, pageUpdate{6, traversalInternalPage(t, []string{string(c.key)}, []uint64{4, 8})})
			appended = append(appended, traversalInternalPage(t, nil, []uint64{5}), traversalInternalPage(t, []string{string(e.key)}, []uint64{6, 9}))
		}
	case "OversizedNewRoot":
		a = rootSplitEntry("a", 995)
		insert = record(string(bytes.Repeat([]byte{'b'}, 4075)), "")
		c = record(string(bytes.Repeat([]byte{'c'}, 100)), "")
		root, newRoot, newCount = 1, 6, 7
		pages = [][]byte{leafPage(a, c)}
		entries = []testEntry{a, insert, c}
		updates = []pageUpdate{{1, leafPage(a)}}
		appended = [][]byte{
			leafPage(insert), leafPage(c),
			traversalInternalPage(t, []string{string(insert.key)}, []uint64{1, 2}),
			traversalInternalPage(t, nil, []uint64{3}),
			traversalInternalPage(t, []string{string(c.key)}, []uint64{4, 5}),
		}
	default:
		t.Fatalf("unknown fixture %q", name)
	}
	db, path := newTraversalDatabase(t, root, pages)
	location, err := db.findLeaf(insert.key)
	require.NoError(t, err)
	location.leaf.Put(insert.key, insert.value)
	header := *db.header
	header.RootPageID, header.PageCount = newRoot, newCount
	return treeSplitFixture{db, path, location, &treeSplitPlan{updates: updates, appendedPages: appended, header: header}, entries, insert}
}

func TestPrepareTreeSplit_PlansCompleteTreeWithoutWriting(t *testing.T) {
	for _, name := range treeSplitCases() {
		t.Run(name, func(t *testing.T) {
			fixture := newTreeSplitFixture(t, name)
			db := fixture.db
			before, err := os.ReadFile(fixture.path)
			require.NoError(t, err)
			headerPointer, headerBefore := db.header, *db.header
			parentsBefore := append([]uint64(nil), fixture.location.parents...)
			plan, err := db.prepareTreeSplit(fixture.location)
			require.NoError(t, err)
			require.NotNil(t, plan)
			assert.Equal(t, fixture.want, plan)
			assert.Same(t, headerPointer, db.header)
			assert.Equal(t, headerBefore, *db.header)
			assert.Equal(t, parentsBefore, fixture.location.parents)
			assertFileContents(t, fixture.path, before)
			for _, entry := range fixture.entries {
				if value, _, found := fixture.location.leaf.Get(entry.key); found {
					assert.True(t, bytes.Equal(entry.value, value))
				}
			}

			contents := bytes.Clone(before)
			seen := make(map[uint64]bool)
			for _, update := range plan.updates {
				require.NotZero(t, update.pageID)
				require.Less(t, update.pageID, headerBefore.PageCount)
				require.False(t, seen[update.pageID], "duplicate update for page %d", update.pageID)
				seen[update.pageID] = true
				require.Len(t, update.page, 4096)
				copy(contents[update.pageID*4096:], update.page)
			}
			for _, page := range plan.appendedPages {
				require.Len(t, page, 4096)
				contents = append(contents, page...)
			}
			headerPage, err := pager.EncodeHeader(&plan.header)
			require.NoError(t, err)
			copy(contents, headerPage)
			require.Equal(t, plan.header.PageCount*4096, uint64(len(contents)))
			resultPath := writeDatabaseFixture(t, contents)
			require.NoError(t, Validate(resultPath))
			for pass := 0; pass < 2; pass++ {
				result, err := Open(resultPath)
				require.NoError(t, err)
				t.Cleanup(func() { _ = result.Close() })
				for i, entry := range fixture.entries {
					value, found, err := result.Get(entry.key)
					require.NoError(t, err)
					require.True(t, found, "entry %d", i)
					assert.True(t, bytes.Equal(entry.value, value), "entry %d", i)
				}
				for _, key := range [][]byte{nil, []byte("bb"), []byte("zz")} {
					_, found, err := result.Get(key)
					require.NoError(t, err)
					assert.False(t, found)
				}
				require.NoError(t, result.Close())
			}
			again, err := db.prepareTreeSplit(fixture.location)
			require.NoError(t, err)
			assert.Equal(t, plan, again)
			plan.updates[0].page[0] = 0xff
			plan.appendedPages[0][0] = 0xff
			plan.header.PageCount++
			assert.Equal(t, fixture.want, again)
			assert.Equal(t, headerBefore, *db.header)
			assertFileContents(t, fixture.path, before)
		})
	}
}

func TestPrepareTreeSplit_RejectsFailuresWithoutWriting(t *testing.T) {
	cases := []struct {
		name    string
		change  func(*testing.T, *treeSplitFixture)
		want    error
		context string
	}{
		{"AlreadyFits", func(t *testing.T, f *treeSplitFixture) {
			leaf, err := pager.NewBPlusLeaf(nil, nil)
			require.NoError(t, err)
			f.location.leaf = leaf
		}, nil, "already fits"},
		{"NilLeaf", func(t *testing.T, f *treeSplitFixture) { f.location.leaf = nil }, nil, "prepare leaf split"},
		{"NonemptyLeafFits", func(t *testing.T, f *treeSplitFixture) {
			f.location.leaf, _ = splitPlanFixture(t, []int{10, 20})
		}, nil, "already fits"},
		{"ExactFit", func(t *testing.T, f *treeSplitFixture) {
			f.location.leaf, _ = splitPlanFixture(t, []int{4088})
		}, nil, "already fits"},
		{"OversizedKey", func(t *testing.T, f *treeSplitFixture) {
			f.location.leaf.Put(bytes.Repeat([]byte{'b'}, 4076), nil)
		}, pager.ErrPagerEntryTooLarge, "prepare leaf split"},
		{"OversizedEntry", func(t *testing.T, f *treeSplitFixture) {
			f.location.leaf.Put([]byte("b"), make([]byte, 4096))
		}, pager.ErrPagerEntryTooLarge, "prepare leaf split"},
		{"ClosedPager", func(t *testing.T, f *treeSplitFixture) { require.NoError(t, f.db.Close()) }, os.ErrClosed, "read parent page"},
		{"MissingParent", func(t *testing.T, f *treeSplitFixture) { f.location.parents = []uint64{100} }, io.EOF, "read parent page"},
		{"LeafAsParent", func(t *testing.T, f *treeSplitFixture) { f.location.parents = []uint64{1} }, pager.ErrPagerCorruptPage, "decode parent page"},
		{"ChildNotInParent", func(t *testing.T, f *treeSplitFixture) { f.location.parents = []uint64{5} }, pager.ErrPagerInvalidArgument, "insert split into parent"},
		{"FailureAfterParentSplit", func(t *testing.T, f *treeSplitFixture) { f.location.parents[0] = 1 }, pager.ErrPagerCorruptPage, "decode parent page"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTreeSplitFixture(t, "CascadeToNewRoot")
			before, err := os.ReadFile(fixture.path)
			require.NoError(t, err)
			headerBefore := *fixture.db.header
			tc.change(t, &fixture)
			plan, err := fixture.db.prepareTreeSplit(fixture.location)
			require.Error(t, err)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			}
			assert.Contains(t, err.Error(), tc.context)
			assert.Nil(t, plan)
			assert.Equal(t, headerBefore, *fixture.db.header)
			assertFileContents(t, fixture.path, before)
		})
	}
}
