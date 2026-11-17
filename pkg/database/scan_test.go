package database

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/pager"
)

func TestDatabase_ScanBoundsAndPruning(t *testing.T) {
	db, _ := newTraversalDatabase(t, 7, traversalPages(t))

	cases := []struct {
		start, end []byte
		limit      int
		want       []string
		pages      uint64
	}{
		{nil, nil, 20, []string{"ant", "bee", "cat", "catalog", "dog", "fox", "yak", "zebra"}, 7},
		{[]byte("cat"), []byte("dog"), 20, []string{"cat", "catalog"}, 3},
		{[]byte("car"), []byte("yak"), 20, []string{"cat", "catalog", "dog", "fox"}, 6},
		{[]byte("zebra"), nil, 20, []string{"zebra"}, 3},
		{[]byte("zz"), nil, 20, nil, 3},
		{nil, nil, 1, []string{"ant"}, 3},
		{nil, []byte{}, 20, nil, 0},
		{[]byte("cat"), []byte("cat"), 20, nil, 0},
	}
	for _, tc := range cases {
		before := db.Stats().PageReads
		entries, err := db.Scan(tc.start, tc.end, tc.limit)
		require.NoError(t, err)
		require.Equal(t, tc.pages, db.Stats().PageReads-before)

		var keys []string

		for _, entry := range entries {
			keys = append(keys, string(entry.Key))
			require.Equal(t, "value:"+string(entry.Key), string(entry.Value))
		}

		require.Equal(t, tc.want, keys)
	}
}

func TestDatabase_ScanAfterSplitsAndReopen(t *testing.T) {
	db, path := openWriterDatabase(t)
	entries := make([]Entry, 80)

	for i := range entries {
		entries[i] = Entry{Key: append(fmt.Appendf(nil, "%04d", i), bytes.Repeat([]byte{'k'}, 996)...), Value: bytes.Repeat([]byte{'v'}, 1000)}
	}

	for i := range entries {
		entry := entries[(i*37)%len(entries)]
		require.NoError(t, db.Put(entry.Key, entry.Value))
	}

	require.Positive(t, db.Stats().SplitOperations)
	require.NoError(t, db.Close())
	reopened, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	got, err := reopened.Scan(entries[20].Key, entries[60].Key, MaxScanEntries)
	require.NoError(t, err)
	require.Equal(t, entries[20:60], got)
	clear(got[0].Key)
	clear(got[0].Value)
	again, err := reopened.Scan(entries[20].Key, entries[60].Key, 1)
	require.NoError(t, err)
	require.Equal(t, entries[20:21], again)
}

func TestDatabase_ScanEmptyKeysAndInvalidArguments(t *testing.T) {
	db, _ := openWriterDatabase(t)
	entries, err := db.Scan(nil, nil, 1)
	require.NoError(t, err)
	require.Nil(t, entries)
	require.NoError(t, db.Put(nil, []byte("empty")))
	entries, err = db.Scan([]byte{}, []byte{0}, 1)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Empty(t, entries[0].Key)
	require.Equal(t, []byte("empty"), entries[0].Value)
	before := db.Stats().PageReads

	for _, limit := range []int{-1, 0, MaxScanEntries + 1} {
		entries, err := db.Scan(nil, nil, limit)
		require.ErrorIs(t, err, ErrInvalidScan)
		require.Nil(t, entries)
	}

	entries, err = db.Scan([]byte("z"), []byte("a"), 1)
	require.ErrorIs(t, err, ErrInvalidScan)
	require.Nil(t, entries)
	require.Equal(t, before, db.Stats().PageReads)
	require.NoError(t, db.Close())
	entries, err = db.Scan(nil, nil, 1)
	require.ErrorIs(t, err, ErrClosed)
	require.Nil(t, entries)
}

func TestDatabase_ScanCorruptTraversal(t *testing.T) {
	cases := []struct {
		name  string
		root  uint64
		pages [][]byte
	}{
		{"Cycle", 1, [][]byte{traversalInternalPage(t, nil, []uint64{1})}},
		{"OutOfRange", 1, [][]byte{traversalInternalPage(t, nil, []uint64{99})}},
		{"RepeatedChild", 3, [][]byte{
			traversalInternalPage(t, nil, []uint64{2}), traversalLeafPage(t, "a"),
			traversalInternalPage(t, []string{"m"}, []uint64{1, 2}),
		}},
		{"OutOfOrder", 3, [][]byte{
			traversalLeafPage(t, "z"), traversalLeafPage(t, "a"),
			traversalInternalPage(t, []string{"m"}, []uint64{1, 2}),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := newTraversalDatabase(t, tc.root, tc.pages)
			entries, err := db.Scan(nil, nil, MaxScanEntries)
			require.ErrorIs(t, err, pager.ErrPagerCorruptPage)
			require.Nil(t, entries)
		})
	}
}

func TestDatabase_ScanIOFailureDiscardsPartialResults(t *testing.T) {
	db, _ := newTraversalDatabase(t, 7, traversalPages(t))
	store := db.pager
	cause := errors.New("scan read failure")
	reads := 0
	db.pager = &hookedStore{pageStore: store, read: func(id uint64) ([]byte, error) {
		reads++
		if reads == 4 {
			return nil, cause
		}

		return store.ReadPage(id)
	}}
	entries, err := db.Scan(nil, nil, 20)
	require.ErrorIs(t, err, cause)
	require.Nil(t, entries)
	require.Equal(t, uint64(1), db.Stats().ReadErrors)
}

func TestPrefixEnd(t *testing.T) {
	for _, tc := range []struct{ prefix, end []byte }{
		{nil, nil},
		{[]byte{0xff, 0xff}, nil},
		{[]byte{1, 2}, []byte{1, 3}},
		{[]byte{1, 0xff}, []byte{2}},
		{[]byte{1, 0, 0xff}, []byte{1, 1}},
	} {
		before := bytes.Clone(tc.prefix)
		require.Equal(t, tc.end, prefixEnd(tc.prefix))
		require.Equal(t, before, tc.prefix)
	}
}
