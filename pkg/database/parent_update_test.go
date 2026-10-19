package database

import (
	"bytes"
	"os"
	"testing"

	"github.com/Noah-Wilderom/godb/pkg/pager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareTreeSplit_PreservesSiblingsAndSelectsImmediateParent(t *testing.T) {
	cases := []struct {
		name, first, second string
		parentID            uint64
		keys                []string
		children            []uint64
	}{
		{"LeftChildOfLeftParent", "ant", "bee", 5, []string{"bee", "cat"}, []uint64{1, 8, 2}},
		{"RightChildOfLeftParent", "cat", "catalog", 5, []string{"cat", "catalog"}, []uint64{1, 2, 8}},
		{"LeftChildOfRightParent", "dog", "fox", 6, []string{"fox", "yak"}, []uint64{3, 8, 4}},
		{"RightChildOfRightParent", "yak", "zebra", 6, []string{"yak", "zebra"}, []uint64{3, 4, 8}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, path := newTraversalDatabase(t, 7, traversalPages(t))
			location, err := db.findLeaf([]byte(tc.first))
			require.NoError(t, err)
			location.leaf.Put([]byte(tc.first), make([]byte, 2000))
			location.leaf.Put([]byte(tc.second), make([]byte, 2084))
			plan, err := prepareLeafSplit(location.leaf)
			require.NoError(t, err)
			require.Len(t, plan.pages, 2)
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			headerBefore := *db.header
			pagesBefore := bytes.Join(plan.pages, nil)
			treePlan, err := db.prepareTreeSplit(location)
			require.NoError(t, err)
			require.Len(t, treePlan.updates, 2)
			parentID, page := treePlan.updates[1].pageID, treePlan.updates[1].page
			assert.Equal(t, uint64(7), treePlan.header.RootPageID)
			assert.Equal(t, uint64(9), treePlan.header.PageCount)
			assert.Equal(t, tc.parentID, parentID)
			assert.Equal(t, traversalInternalPage(t, tc.keys, tc.children), page)
			parent, err := pager.DecodeInternal(page)
			require.NoError(t, err)
			assert.Equal(t, location.pageID, parent.ChildFor([]byte(tc.first)))
			assert.Equal(t, uint64(8), parent.ChildFor([]byte(tc.second)))
			assert.Equal(t, []uint64{7, tc.parentID}, location.parents)
			assert.Equal(t, [][]byte{[]byte(tc.second)}, plan.separators)
			assert.Equal(t, pagesBefore, bytes.Join(plan.pages, nil))
			assert.Equal(t, headerBefore, *db.header)
			assertFileContents(t, path, before)
		})
	}
}

func TestPrepareTreeSplit_AddsMultipleChildren(t *testing.T) {
	db, path := newTraversalDatabase(t, 7, traversalPages(t))
	location, err := db.findLeaf([]byte("ant"))
	require.NoError(t, err)
	location.leaf.Put([]byte("ant"), make([]byte, 1993))
	location.leaf.Put([]byte("bat"), make([]byte, 2993))
	location.leaf.Put([]byte("bee"), make([]byte, 1993))
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	plan, err := db.prepareTreeSplit(location)
	require.NoError(t, err)
	require.Len(t, plan.updates, 2)
	require.Len(t, plan.appendedPages, 2)
	assert.Equal(t, uint64(5), plan.updates[1].pageID)
	assert.Equal(t, traversalInternalPage(t, []string{"bat", "bee", "cat"}, []uint64{1, 8, 9, 2}), plan.updates[1].page)
	assert.Equal(t, uint64(7), plan.header.RootPageID)
	assert.Equal(t, uint64(10), plan.header.PageCount)
	assert.Equal(t, uint64(8), db.header.PageCount)
	assertFileContents(t, path, before)
}
