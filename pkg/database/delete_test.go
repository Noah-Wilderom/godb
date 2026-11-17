package database

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/pager"
)

func TestDatabase_DeleteAcrossInternalSplitsAndReopen(t *testing.T) {
	db, path := openWriterDatabase(t)
	keys := make([][]byte, 80)

	for i := range keys {
		keys[i] = append(fmt.Appendf(nil, "%04d", i), bytes.Repeat([]byte{'k'}, 996)...)
		require.NoError(t, db.Put(keys[i], bytes.Repeat([]byte{'v'}, 1000)))
	}

	location, err := db.findLeaf(keys[0])
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(location.parents), 2)
	pages := db.header.PageCount
	root := db.header.RootPageID
	before := db.Stats()

	for i := range keys {
		deleted, err := db.Delete(keys[i*37%len(keys)])
		require.NoError(t, err)
		require.True(t, deleted)
	}

	require.Equal(t, uint64(len(keys)), db.Stats().Deletes-before.Deletes)
	require.Equal(t, pages, db.header.PageCount)
	require.Equal(t, root, db.header.RootPageID)
	require.Equal(t, before.PageAppends, db.Stats().PageAppends)
	entries, err := db.Scan(nil, nil, MaxScanEntries)
	require.NoError(t, err)
	require.Empty(t, entries)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
	db, err = Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	for _, key := range keys {
		_, found, err := db.Get(key)
		require.NoError(t, err)
		require.False(t, found)
		require.NoError(t, db.Put(key, []byte("reused")))
	}

	require.Equal(t, pages, db.header.PageCount)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestDatabase_DeleteBatchMissingDuplicatesAndEmptyKey(t *testing.T) {
	db, path := openWriterDatabase(t)
	require.NoError(t, db.Put(nil, nil))
	require.NoError(t, db.Put([]byte("a"), []byte("value")))
	before := db.Stats()
	deleted, err := db.DeleteBatch([][]byte{nil, {}, []byte("a"), []byte("missing"), []byte("a")})
	require.NoError(t, err)
	require.Equal(t, 2, deleted)
	require.Equal(t, before.Syncs+1, db.Stats().Syncs)
	require.Equal(t, before.Writes+2, db.Stats().Writes)
	require.Equal(t, uint64(2), db.Stats().Deletes)
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	before = db.Stats()
	deleted, err = db.DeleteBatch([][]byte{nil, []byte("missing")})
	require.NoError(t, err)
	require.Zero(t, deleted)
	deleted, err = db.DeleteBatch(nil)
	require.NoError(t, err)
	require.Zero(t, deleted)
	require.Equal(t, before.Syncs, db.Stats().Syncs)
	require.Equal(t, before.PageWrites, db.Stats().PageWrites)
	assertFileContents(t, path, contents)
}

func TestDatabase_DeleteRejectsInvalidInputAndClosed(t *testing.T) {
	db, _ := openWriterDatabase(t)
	deleted, err := db.DeleteBatch(make([][]byte, MaxBatchEntries+1))
	require.ErrorIs(t, err, ErrBatchTooLarge)
	require.Zero(t, deleted)
	found, err := db.Delete(make([]byte, 4096))
	require.ErrorIs(t, err, pager.ErrPagerEntryTooLarge)
	require.False(t, found)
	require.Zero(t, db.Stats().PageWrites)
	require.NoError(t, db.Close())
	found, err = db.Delete(nil)
	require.ErrorIs(t, err, ErrClosed)
	require.False(t, found)
	deleted, err = db.DeleteBatch(nil)
	require.ErrorIs(t, err, ErrClosed)
	require.Zero(t, deleted)
}

func TestDatabase_DeleteConcurrentSingleWinner(t *testing.T) {
	db, _ := openWriterDatabase(t)
	require.NoError(t, db.Put([]byte("key"), nil))
	start := make(chan struct{})
	results := make(chan bool, 32)
	errors := make(chan error, 32)

	var wg sync.WaitGroup

	for range 32 {
		wg.Go(func() {
			<-start
			deleted, err := db.Delete([]byte("key"))
			results <- deleted
			errors <- err
		})
	}

	close(start)
	wg.Wait()
	winners := 0

	for range 32 {
		require.NoError(t, <-errors)

		if <-results {
			winners++
		}
	}

	require.Equal(t, 1, winners)
	require.Equal(t, uint64(1), db.Stats().Deletes)
}

func TestDatabase_DeleteMixedGroupOrderAndOwnership(t *testing.T) {
	db, _ := openWriterDatabase(t)
	entered, release := blockFirstFlush(t, db)
	first := make(chan error, 1)
	go func() { first <- db.Put([]byte("initial"), nil) }()
	<-entered
	put, err := makeWriteRequest([]Entry{{Key: []byte("key"), Value: []byte("one")}})
	require.NoError(t, err)
	key := []byte("key")
	del, err := makeDeleteRequest([][]byte{key})
	require.NoError(t, err)
	clear(key)
	missing, err := makeDeleteRequest([][]byte{[]byte("key")})
	require.NoError(t, err)
	last, err := makeWriteRequest([]Entry{{Key: []byte("key"), Value: []byte("two")}})
	require.NoError(t, err)

	for _, request := range []*writeRequest{put, del, missing, last} {
		db.writes <- request
	}

	release()
	require.NoError(t, waitWriteResult(t, first))

	for _, request := range []*writeRequest{put, del, missing, last} {
		require.NoError(t, waitWriteResult(t, request.result))
	}

	require.Equal(t, 1, del.deleted)
	require.Zero(t, missing.deleted)
	require.Equal(t, uint64(2), db.Stats().Syncs)
	value, found, err := db.Get([]byte("key"))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []byte("two"), value)
}

func TestDatabase_DeleteStorageFailures(t *testing.T) {
	for _, stage := range []string{"Read", "Write", "Sync"} {
		t.Run(stage, func(t *testing.T) {
			db, _ := openWriterDatabase(t)
			require.NoError(t, db.Put([]byte("key"), nil))
			cause := errors.New("injected delete failure")
			hooks := &hookedStore{pageStore: db.pager}

			switch stage {
			case "Read":
				hooks.read = func(uint64) ([]byte, error) { return nil, cause }
			case "Write":
				hooks.write = func(uint64, []byte) error { return cause }
			case "Sync":
				hooks.sync = func() error { return cause }
			}

			db.pager = hooks
			deleted, err := db.Delete([]byte("key"))
			require.ErrorIs(t, err, cause)
			require.ErrorIs(t, err, ErrWriteFailed)
			require.False(t, deleted)
			require.Zero(t, db.Stats().Deletes)
			_, err = db.DeleteBatch(nil)
			require.ErrorIs(t, err, ErrWriteFailed)
			require.ErrorIs(t, db.Put([]byte("later"), nil), ErrWriteFailed)
		})
	}
}
