package database

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/pager"
)

func TestValidate_TraversesEntireTree(t *testing.T) {
	for name, mutate := range map[string]func([][]byte){
		"cycle":                    func(p [][]byte) { binary.LittleEndian.PutUint64(p[5][3:11], 7) },
		"shared leaf":              func(p [][]byte) { binary.LittleEndian.PutUint64(p[5][3:11], 1) },
		"out of range child":       func(p [][]byte) { binary.LittleEndian.PutUint64(p[5][3:11], 8) },
		"malformed last leaf":      func(p [][]byte) { p[3][0] = 0xff },
		"misrouted leaf":           func(p [][]byte) { p[6] = traversalInternalPage(t, []string{"eel"}, []uint64{5, 6}) },
		"separator outside parent": func(p [][]byte) { p[4] = traversalInternalPage(t, []string{"yak"}, []uint64{1, 2}) },
		"duplicate leaf key":       func(p [][]byte) { p[3] = traversalLeafPage(t, "fox", "zebra") },
		"unequal depths":           func(p [][]byte) { p[6] = traversalInternalPage(t, []string{"dog"}, []uint64{5, 3}) },
	} {
		t.Run(name, func(t *testing.T) {
			pages := traversalPages(t)
			mutate(pages)
			db, path := newTraversalDatabase(t, 7, pages)
			require.NoError(t, db.Close())
			require.ErrorIs(t, Validate(path), pager.ErrPagerCorruptPage)
		})
	}
}

func TestValidate_EmptySeparatorBounds(t *testing.T) {
	for _, misplaced := range []bool{false, true} {
		left := traversalLeafPage(t)
		if misplaced {
			left = traversalLeafPage(t, "")
		}

		pages := [][]byte{
			left,
			traversalLeafPage(t, "", "a"),
			traversalInternalPage(t, []string{""}, []uint64{1, 2}),
		}
		db, path := newTraversalDatabase(t, 3, pages)
		require.NoError(t, db.Close())

		if misplaced {
			require.ErrorIs(t, Validate(path), pager.ErrPagerCorruptPage)
		} else {
			require.NoError(t, Validate(path))
		}
	}
}
