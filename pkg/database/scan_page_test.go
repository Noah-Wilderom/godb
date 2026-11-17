package database

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDatabase_ScanPageTraversalAndTerminalPages(t *testing.T) {
	db, _ := newTraversalDatabase(t, 7, traversalPages(t))
	want, err := db.Scan(nil, nil, MaxScanEntries)
	require.NoError(t, err)

	for _, limit := range []int{1, 2, 3, 8, 9} {
		var (
			cursor *ScanCursor
			got    []Entry
		)

		for pageNumber := range 20 {
			page, err := db.ScanPage(nil, nil, limit, cursor)
			require.NoError(t, err)
			require.NotEmpty(t, page.Entries)
			require.LessOrEqual(t, len(page.Entries), limit)
			got = append(got, page.Entries...)

			if page.Next == nil {
				break
			}

			require.Less(t, pageNumber, 19, "pagination must terminate")
			cursor = page.Next
		}

		require.Equal(t, want, got)
	}

	page, err := db.ScanPage([]byte("cat"), []byte("dog"), 2, nil)
	require.NoError(t, err)
	require.Len(t, page.Entries, 2)
	require.Nil(t, page.Next, "an exactly full final page needs no cursor")
	page, err = db.ScanPage([]byte("zz"), nil, 2, nil)
	require.NoError(t, err)
	require.Equal(t, EntryPage{}, page)
}

func TestDatabase_ScanPageEmptyAndPrefixKeys(t *testing.T) {
	db, _ := openWriterDatabase(t)

	for _, key := range [][]byte{nil, {0}, {0, 0}, []byte("a"), {'a', 0}, {'a', 0, 0}, {0xff}, {0xff, 0}} {
		require.NoError(t, db.Put(key, []byte("value")))
	}

	want, err := db.Scan(nil, nil, 20)
	require.NoError(t, err)

	var (
		cursor *ScanCursor
		got    []Entry
	)

	for range len(want) {
		page, err := db.ScanPage(nil, nil, 1, cursor)
		require.NoError(t, err)
		require.Len(t, page.Entries, 1)
		got = append(got, page.Entries...)
		cursor = page.Next
	}

	require.Nil(t, cursor)
	require.Equal(t, want, got)
}

func TestDatabase_ScanPageMaximumLimit(t *testing.T) {
	db, _ := openWriterDatabase(t)
	entries := make([]Entry, MaxScanEntries+1)

	for i := range entries {
		entries[i] = Entry{Key: fmt.Appendf(nil, "%04d", i), Value: []byte("value")}
	}

	for start := 0; start < len(entries); start += MaxBatchEntries {
		require.NoError(t, db.PutBatch(entries[start:min(start+MaxBatchEntries, len(entries))]))
	}

	first, err := db.ScanPage(nil, nil, MaxScanEntries, nil)
	require.NoError(t, err)
	require.Equal(t, entries[:MaxScanEntries], first.Entries)
	require.NotNil(t, first.Next)
	last, err := db.ScanPage(nil, nil, MaxScanEntries, first.Next)
	require.NoError(t, err)
	require.Equal(t, entries[MaxScanEntries:], last.Entries)
	require.Nil(t, last.Next)
}

func TestDatabase_ScanPageCursorOwnershipAndScope(t *testing.T) {
	db, _ := newTraversalDatabase(t, 7, traversalPages(t))
	start, end := []byte("a"), []byte("z")
	first, err := db.ScanPage(start, end, 1, nil)
	require.NoError(t, err)
	require.NotNil(t, first.Next)
	clear(start)
	clear(end)
	clear(first.Entries[0].Key)
	clear(first.Entries[0].Value)
	second, err := db.ScanPage([]byte("a"), []byte("z"), 2, first.Next)
	require.NoError(t, err)
	require.Equal(t, "bee", string(second.Entries[0].Key))
	require.Equal(t, "cat", string(second.Entries[1].Key))
	retry, err := db.ScanPage([]byte("a"), []byte("z"), 2, first.Next)
	require.NoError(t, err)
	require.Equal(t, second.Entries, retry.Entries)

	for _, bounds := range [][2][]byte{{nil, []byte("z")}, {[]byte("a"), nil}} {
		page, err := db.ScanPage(bounds[0], bounds[1], 2, first.Next)
		require.ErrorIs(t, err, ErrInvalidCursor)
		require.Equal(t, EntryPage{}, page)
	}

	page, err := db.ScanPage(nil, nil, 1, &ScanCursor{})
	require.ErrorIs(t, err, ErrInvalidCursor)
	require.Equal(t, EntryPage{}, page)
	other, _ := openWriterDatabase(t)
	page, err = other.ScanPage([]byte("a"), []byte("z"), 2, first.Next)
	require.ErrorIs(t, err, ErrInvalidCursor)
	require.Equal(t, EntryPage{}, page)
	all, err := db.ScanPage(nil, nil, 1, nil)
	require.NoError(t, err)
	page, err = db.ScanPage(nil, []byte{}, 1, all.Next)
	require.ErrorIs(t, err, ErrInvalidCursor)
	require.Equal(t, EntryPage{}, page)
}

func TestDatabase_ScanPageWritesBetweenCalls(t *testing.T) {
	db, _ := openWriterDatabase(t)
	require.NoError(t, db.Put([]byte("a"), make([]byte, 3000)))
	require.NoError(t, db.Put([]byte("z"), make([]byte, 3000)))
	first, err := db.ScanPage(nil, nil, 1, nil)
	require.NoError(t, err)
	require.NotNil(t, first.Next)
	require.NoError(t, db.Put(nil, []byte("before cursor")))
	require.NoError(t, db.Put([]byte("a"), []byte("updated behind cursor")))
	require.NoError(t, db.Put([]byte{'a', 0}, make([]byte, 3000)))
	require.NoError(t, db.Put([]byte("m"), make([]byte, 3000)))
	second, err := db.ScanPage(nil, nil, 10, first.Next)
	require.NoError(t, err)
	require.Len(t, second.Entries, 3)
	require.Equal(t, []byte{'a', 0}, second.Entries[0].Key)
	require.Equal(t, []byte("m"), second.Entries[1].Key)
	require.Equal(t, []byte("z"), second.Entries[2].Key)
	require.Nil(t, second.Next)
	before := bytes.Clone(first.Entries[0].Value)
	require.Equal(t, make([]byte, 3000), before)
}

func TestDatabase_ScanPageFailuresReturnNoCursor(t *testing.T) {
	db, _ := newTraversalDatabase(t, 7, traversalPages(t))
	store := db.pager
	cause := errors.New("lookahead failed")
	reads := 0
	db.pager = &hookedStore{pageStore: store, read: func(id uint64) ([]byte, error) {
		reads++
		if reads == 4 {
			return nil, cause
		}

		return store.ReadPage(id)
	}}
	page, err := db.ScanPage(nil, nil, 2, nil)
	require.ErrorIs(t, err, cause)
	require.Equal(t, EntryPage{}, page)

	for _, limit := range []int{0, MaxScanEntries + 1} {
		page, err := db.ScanPage(nil, nil, limit, nil)
		require.ErrorIs(t, err, ErrInvalidScan)
		require.Equal(t, EntryPage{}, page)
	}

	db.pager = store
	first, err := db.ScanPage(nil, nil, 1, nil)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	page, err = db.ScanPage(nil, nil, 1, first.Next)
	require.ErrorIs(t, err, ErrClosed)
	require.Equal(t, EntryPage{}, page)
}

func TestDatabase_ScanPageCursorConcurrentReuse(t *testing.T) {
	db, _ := newTraversalDatabase(t, 7, traversalPages(t))
	first, err := db.ScanPage(nil, nil, 2, nil)
	require.NoError(t, err)
	want, err := db.ScanPage(nil, nil, 3, first.Next)
	require.NoError(t, err)

	type result struct {
		page EntryPage
		err  error
	}

	results := make(chan result, 16)

	var wg sync.WaitGroup

	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			page, err := db.ScanPage(nil, nil, 3, first.Next)
			results <- result{page, err}
		}()
	}

	wg.Wait()
	close(results)

	for result := range results {
		require.NoError(t, result.err)
		require.Equal(t, want.Entries, result.page.Entries)
	}
}
