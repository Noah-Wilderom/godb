package database

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/pager"
)

type hookedStore struct {
	pageStore

	write  func(uint64, []byte) error
	append func([]byte) (uint64, error)
	sync   func() error
}

func (p *hookedStore) WritePage(id uint64, page []byte) error {
	if p.write != nil {
		return p.write(id, page)
	}

	return p.pageStore.WritePage(id, page)
}

func (p *hookedStore) AppendPage(page []byte) (uint64, error) {
	if p.append != nil {
		return p.append(page)
	}

	return p.pageStore.AppendPage(page)
}

func (p *hookedStore) Flush() error {
	if p.sync != nil {
		return p.sync()
	}

	return p.pageStore.Flush()
}

func openWriterDatabase(t testing.TB) (*Database, string) {
	t.Helper()
	path := writeDatabaseFixture(t, validDatabaseBytes())
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	return db, path
}

func waitWriteResult(t *testing.T, result <-chan error) error {
	t.Helper()

	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not complete")
		return nil
	}
}

func blockFirstFlush(t *testing.T, db *Database) (<-chan struct{}, func()) {
	t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})

	var once sync.Once

	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	store := db.pager
	first := true
	db.pager = &hookedStore{pageStore: store, sync: func() error {
		if first {
			first = false
			close(entered)
			<-release
		}

		return store.Flush()
	}}
	return entered, unblock
}

func TestDatabase_PutBatchPreservesOrderAndSyncsOnce(t *testing.T) {
	db, path := openWriterDatabase(t)
	entries := []Entry{
		{Key: []byte("a"), Value: []byte("first")},
		{Key: []byte("b"), Value: []byte("other")},
		{Key: []byte("a"), Value: []byte("last")},
	}
	require.NoError(t, db.PutBatch(entries))
	stats := db.Stats()
	assert.Equal(t, uint64(3), stats.Writes)
	assert.Equal(t, uint64(1), stats.WriteRequests)
	assert.Equal(t, uint64(1), stats.WriteBatches)
	assert.Equal(t, uint64(1), stats.Syncs)
	require.NoError(t, db.Close())

	reopened, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, reopened.Close()) })

	for key, want := range map[string]string{"a": "last", "b": "other"} {
		value, found, err := reopened.Get([]byte(key))
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, want, string(value))
	}
}

func TestDatabase_PutBatchRejectsInvalidInputBeforeWriting(t *testing.T) {
	db, path := openWriterDatabase(t)
	invalid := []Entry{{Key: []byte("valid"), Value: []byte("value")}, {Key: bytes.Repeat([]byte{'x'}, 4096)}}
	require.ErrorIs(t, db.PutBatch(invalid), pager.ErrPagerEntryTooLarge)
	require.ErrorIs(t, db.PutBatch(make([]Entry, MaxBatchEntries+1)), ErrBatchTooLarge)
	require.NoError(t, db.PutBatch(nil))
	assert.Zero(t, db.Stats().PageWrites)
	assert.Zero(t, db.Stats().Syncs)
	assert.Equal(t, uint64(2), db.Stats().WriteErrors)
	assertFileContents(t, path, validDatabaseBytes())
	require.NoError(t, db.Put([]byte("valid"), nil))
	require.NoError(t, db.Close())
	require.ErrorIs(t, db.PutBatch(nil), ErrClosed)
}

func TestDatabase_QueuedBatchesAreBoundedAndKeepOrder(t *testing.T) {
	for _, sizes := range [][]int{{1, 1, 1, 1}, {40, 30, 1}, {64, 64, 1}} {
		t.Run(fmt.Sprint(sizes), func(t *testing.T) {
			db, _ := openWriterDatabase(t)
			entered, release := blockFirstFlush(t, db)
			first := make(chan error, 1)
			go func() { first <- db.Put([]byte("key"), []byte("initial")) }()
			<-entered

			var requests []*writeRequest

			count := 0

			for _, size := range sizes {
				entries := make([]Entry, size)
				for i := range entries {
					count++
					entries[i] = Entry{Key: []byte("key"), Value: fmt.Appendf(nil, "%d", count)}
				}

				request, err := makeWriteRequest(entries)
				require.NoError(t, err)
				db.writes <- request
				requests = append(requests, request)
			}

			release()
			require.NoError(t, waitWriteResult(t, first))

			for _, request := range requests {
				require.NoError(t, waitWriteResult(t, request.result))
			}

			wantBatches := uint64(2)
			if sizes[0] == 40 {
				wantBatches = 3
			}

			if sizes[0] == 64 {
				wantBatches = 4
			}

			assert.Equal(t, wantBatches, db.Stats().WriteBatches)
			assert.Equal(t, wantBatches, db.Stats().Syncs)
			assert.Equal(t, uint64(count+1), db.Stats().Writes)
			value, found, err := db.Get([]byte("key"))
			require.NoError(t, err)
			assert.True(t, found)
			assert.Equal(t, fmt.Sprint(count), string(value))
		})
	}
}

func TestDatabase_BatchWaitsForSyncBeforeAcknowledgingOrReading(t *testing.T) {
	db, _ := openWriterDatabase(t)
	entered, release := blockFirstFlush(t, db)
	write := make(chan error, 1)
	go func() { write <- db.Put([]byte("key"), []byte("value")) }()
	<-entered
	assert.Zero(t, db.Stats().Writes)
	assert.Zero(t, db.Stats().Syncs)

	if db.mu.TryRLock() {
		db.mu.RUnlock()
		t.Error("read lock must remain unavailable until sync completes")
	}

	select {
	case err := <-write:
		t.Fatalf("write returned before sync: %v", err)
	default:
	}

	release()
	require.NoError(t, waitWriteResult(t, write))
	value, found, err := db.Get([]byte("key"))
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, []byte("value"), value)
}

func TestDatabase_CloseDrainsFullQueue(t *testing.T) {
	db, path := openWriterDatabase(t)
	entered, release := blockFirstFlush(t, db)
	first := make(chan error, 1)
	go func() { first <- db.Put([]byte("key"), []byte("initial")) }()
	<-entered

	requests := make([]*writeRequest, writeQueueSize)
	for i := range requests {
		request, err := makeWriteRequest([]Entry{{Key: []byte("key"), Value: []byte("queued")}})
		require.NoError(t, err)
		requests[i] = request
		db.writes <- request
	}

	blockedPut := make(chan error, 1)
	go func() { blockedPut <- db.Put([]byte("key"), []byte("queued")) }()
	closed := make(chan error, 1)
	go func() { closed <- db.Close() }()
	release()
	require.NoError(t, waitWriteResult(t, first))

	for _, request := range requests {
		require.NoError(t, waitWriteResult(t, request.result))
	}

	err := waitWriteResult(t, blockedPut)
	if err != nil {
		require.ErrorIs(t, err, ErrClosed)
	}

	require.NoError(t, waitWriteResult(t, closed))
	require.ErrorIs(t, db.Put([]byte("after"), nil), ErrClosed)
	reopened, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, reopened.Close()) })
	value, found, err := reopened.Get([]byte("key"))
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, []byte("queued"), value)
}

func TestDatabase_WritersAreIndependent(t *testing.T) {
	firstDB, _ := openWriterDatabase(t)
	secondDB, _ := openWriterDatabase(t)
	entered, release := blockFirstFlush(t, firstDB)
	first := make(chan error, 1)
	go func() { first <- firstDB.Put([]byte("key"), []byte("first")) }()
	<-entered
	second := make(chan error, 1)
	go func() { second <- secondDB.Put([]byte("key"), []byte("second")) }()
	require.NoError(t, waitWriteResult(t, second))
	value, found, err := secondDB.Get([]byte("key"))
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, []byte("second"), value)
	release()
	require.NoError(t, waitWriteResult(t, first))
}

func TestDatabase_BatchFailureStopsFurtherOperations(t *testing.T) {
	for _, stage := range []string{"Write", "Append", "Header", "Sync"} {
		t.Run(stage, func(t *testing.T) {
			db, _ := openWriterDatabase(t)
			cause := errors.New("injected storage failure")
			store := db.pager
			hooks := &hookedStore{pageStore: store}

			switch stage {
			case "Write":
				hooks.write = func(uint64, []byte) error { return cause }
			case "Append":
				hooks.append = func([]byte) (uint64, error) { return 0, cause }
			case "Header":
				hooks.write = func(id uint64, page []byte) error {
					if id == 0 {
						return cause
					}

					return store.WritePage(id, page)
				}
			case "Sync":
				hooks.sync = func() error { return cause }
			}

			db.pager = hooks
			entries := []Entry{{Key: []byte("a"), Value: make([]byte, 3000)}, {Key: []byte("b"), Value: make([]byte, 3000)}}
			err := db.PutBatch(entries)
			require.ErrorIs(t, err, cause)
			require.ErrorIs(t, err, ErrWriteFailed)
			before := db.Stats()
			assert.Zero(t, before.Writes)
			assert.Zero(t, before.Syncs)
			assert.Equal(t, uint64(1), before.WriteErrors)
			require.ErrorIs(t, db.Put([]byte("later"), nil), ErrWriteFailed)
			require.ErrorIs(t, db.PutBatch(nil), ErrWriteFailed)
			value, found, err := db.Get([]byte("a"))
			require.ErrorIs(t, err, ErrWriteFailed)
			assert.False(t, found)
			assert.Nil(t, value)
			after := db.Stats()
			assert.Equal(t, before.PageReads, after.PageReads)
			assert.Equal(t, before.PageWrites, after.PageWrites)
			assert.Equal(t, before.WriteBatches, after.WriteBatches)
		})
	}
}

func TestDatabase_WriteRequestOwnsItsInput(t *testing.T) {
	db, _ := openWriterDatabase(t)
	entries := []Entry{{Key: []byte("key"), Value: []byte("value")}}
	request, err := makeWriteRequest(entries)
	require.NoError(t, err)
	entries[0].Key[0] = 'X'
	entries[0].Value[0] = 'X'
	entries[0] = Entry{}
	db.writes <- request
	require.NoError(t, waitWriteResult(t, request.result))
	value, found, err := db.Get([]byte("key"))
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, []byte("value"), value)
}

func TestDatabase_GroupSyncFailureFailsEveryRequest(t *testing.T) {
	db, _ := openWriterDatabase(t)
	entered := make(chan struct{})
	release := make(chan struct{})

	var once sync.Once

	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	store := db.pager
	cause := errors.New("group sync failed")
	firstSync := true
	db.pager = &hookedStore{pageStore: store, sync: func() error {
		if firstSync {
			firstSync = false
			close(entered)
			<-release
			return store.Flush()
		}

		return cause
	}}
	first := make(chan error, 1)
	go func() { first <- db.Put([]byte("key"), []byte("initial")) }()
	<-entered

	var requests []*writeRequest

	for range 4 {
		request, err := makeWriteRequest([]Entry{{Key: []byte("key"), Value: []byte("updated")}})
		require.NoError(t, err)
		db.writes <- request
		requests = append(requests, request)
	}

	unblock()
	require.NoError(t, waitWriteResult(t, first))

	for _, request := range requests {
		err := waitWriteResult(t, request.result)
		require.ErrorIs(t, err, ErrWriteFailed)
		require.ErrorIs(t, err, cause)
	}

	assert.Equal(t, uint64(1), db.Stats().Writes)
	assert.Equal(t, uint64(1), db.Stats().Syncs)
	assert.Equal(t, uint64(1), db.Stats().SyncErrors)
	require.ErrorIs(t, db.Put([]byte("later"), nil), ErrWriteFailed)
}

func TestDatabase_OrderedReplayIsIndependentOfBatchBoundaries(t *testing.T) {
	entries := make([]Entry, 80)
	for i := range entries {
		key := fmt.Appendf(nil, "%04d", (i*37)%80)
		key = append(key, bytes.Repeat([]byte{'k'}, 900)...)
		entries[i] = Entry{Key: key, Value: bytes.Repeat([]byte{byte(i)}, 1000)}
	}

	entries = append(entries, Entry{Key: entries[0].Key, Value: []byte("replacement")})

	var expected []byte

	for _, size := range []int{1, 17, MaxBatchEntries} {
		db, path := openWriterDatabase(t)
		for start := 0; start < len(entries); start += size {
			require.NoError(t, db.PutBatch(entries[start:min(start+size, len(entries))]))
		}

		assert.Positive(t, db.Stats().SplitOperations)
		require.NoError(t, db.Close())
		contents, err := os.ReadFile(path)
		require.NoError(t, err)

		if expected == nil {
			expected = contents
		} else {
			assert.Equal(t, expected, contents)
		}

		reopened, err := Open(path)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, reopened.Close()) })

		for i, entry := range entries[:80] {
			want := entry.Value
			if i == 0 {
				want = []byte("replacement")
			}

			value, found, err := reopened.Get(entry.Key)
			require.NoError(t, err)
			assert.True(t, found)
			assert.Equal(t, want, value)
		}
	}
}

func TestDatabase_ConcurrentWritesAndStatsPreserveEntries(t *testing.T) {
	db, path := openWriterDatabase(t)
	require.NoError(t, db.Put([]byte("seed"), []byte("value")))
	start := make(chan struct{})
	results := make(chan error, 9)

	for writer := range 8 {
		go func() {
			<-start

			for i := range 16 {
				key := fmt.Appendf(nil, "%d:%d", writer, i)
				if err := db.Put(key, bytes.Repeat(key, 100)); err != nil {
					results <- err
					return
				}
			}

			results <- nil
		}()
	}

	go func() {
		<-start

		for range 128 {
			value, found, err := db.Get([]byte("seed"))
			if err != nil || !found || string(value) != "value" {
				results <- fmt.Errorf("concurrent read: found=%t value=%q err=%v", found, value, err)
				return
			}

			_ = db.Stats()
		}

		results <- nil
	}()
	close(start)

	for range 9 {
		require.NoError(t, waitWriteResult(t, results))
	}

	stats := db.Stats()
	assert.Equal(t, uint64(129), stats.Writes)
	assert.Equal(t, uint64(129), stats.WriteRequests)
	assert.Equal(t, uint64(128), stats.Reads)
	assert.Zero(t, stats.ReadErrors)
	assert.Zero(t, stats.WriteErrors)
	assert.Positive(t, stats.SplitOperations)
	require.NoError(t, db.Close())
	reopened, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, reopened.Close()) })

	for writer := range 8 {
		for i := range 16 {
			key := fmt.Appendf(nil, "%d:%d", writer, i)
			value, found, err := reopened.Get(key)
			require.NoError(t, err)
			assert.True(t, found)
			assert.Equal(t, bytes.Repeat(key, 100), value)
		}
	}
}
