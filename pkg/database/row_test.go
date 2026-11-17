package database

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/pager"
	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func assertStoredRow(t testing.TB, db *Database, table schema.Table, row schema.Row) {
	t.Helper()
	plan, err := PlanRowInsert(table, row)
	require.NoError(t, err)
	entries := []Entry{plan.Primary}

	for _, index := range plan.Indexes {
		entries = append(entries, Entry{Key: index.Key, Value: index.Value})
	}

	for _, entry := range entries {
		value, found, err := db.Get(entry.Key)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, entry.Value, value)
	}
}

func numberedRow(id uint64) schema.Row {
	return schema.Row{
		1: recordpkg.NewUint64(id),
		2: recordpkg.NewBytes(fmt.Appendf(nil, "name_%06d", id)),
		3: recordpkg.NewInt64(int64(id)),
	}
}

func TestDatabase_InsertRowPersistsAcrossSplitsAndReopen(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, _ := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))

	for id := uint64(1); id <= 100; id++ {
		require.NoError(t, db.InsertRow(table, numberedRow(id)))
	}

	stats := db.Stats()
	require.Equal(t, uint64(402), stats.Writes)
	require.Equal(t, uint64(101), stats.WriteRequests)
	require.Equal(t, uint64(101), stats.Syncs)
	require.Positive(t, stats.SplitOperations)
	require.NoError(t, db.Close())
	reopened, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })

	for id := uint64(1); id <= 100; id++ {
		assertStoredRow(t, reopened, table, numberedRow(id))
	}

	require.ErrorIs(t, reopened.InsertRow(table, numberedRow(1)), ErrDuplicatePrimaryKey)
	conflict := numberedRow(101)
	conflict[2] = numberedRow(1)[2]
	require.ErrorIs(t, reopened.InsertRow(table, conflict), ErrUniqueConflict)
	require.NoError(t, reopened.InsertRow(table, numberedRow(101)))
}

func TestDatabase_InsertRowConflictsDoNotWriteOrPoison(t *testing.T) {
	for _, primary := range []bool{false, true} {
		t.Run(fmt.Sprintf("Primary%t", primary), func(t *testing.T) {
			db, path := openWriterDatabase(t)
			table, row := rowInsertFixture()
			require.NoError(t, db.CreateTable(table))
			require.NoError(t, db.InsertRow(table, row))
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			stats := db.Stats()
			rejected := numberedRow(999)
			want := ErrUniqueConflict

			if primary {
				rejected[1] = row[1]
				want = ErrDuplicatePrimaryKey
			} else {
				rejected[2] = row[2]
			}

			err = db.InsertRow(table, rejected)
			require.ErrorIs(t, err, want)
			require.NotErrorIs(t, err, ErrWriteFailed)
			after := db.Stats()
			require.Equal(t, stats.Writes, after.Writes)
			require.Equal(t, stats.PageWrites, after.PageWrites)
			require.Equal(t, stats.Syncs, after.Syncs)
			require.Equal(t, stats.WriteErrors+1, after.WriteErrors)
			assertFileContents(t, path, before)
			assertStoredRow(t, db, table, row)
			require.NoError(t, db.InsertRow(table, numberedRow(1000)))
			assertStoredRow(t, db, table, numberedRow(1000))
		})
	}
}

func TestDatabase_InsertRowNonUniqueAndNullableUnique(t *testing.T) {
	for _, null := range []bool{false, true} {
		db, _ := openWriterDatabase(t)
		table, row := rowInsertFixture()

		if null {
			row[2] = recordpkg.NewNull()
		} else {
			table.Indexes[2].Unique = false
		}

		require.NoError(t, db.CreateTable(table))

		for id := uint64(1); id <= 3; id++ {
			row[1], row[3] = recordpkg.NewUint64(id), recordpkg.NewInt64(int64(id))
			require.NoError(t, db.InsertRow(table, row))
		}

		for id := uint64(1); id <= 3; id++ {
			row[1], row[3] = recordpkg.NewUint64(id), recordpkg.NewInt64(int64(id))
			assertStoredRow(t, db, table, row)
		}
	}
}

func TestDatabase_InsertRowUniqueReferenceToSamePrimary(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	plan, err := PlanRowInsert(table, row)
	require.NoError(t, err)

	for _, index := range plan.Indexes {
		if index.RequiresUniqueCheck {
			require.NoError(t, db.Put(index.Key, index.Value))
		}
	}

	require.NoError(t, db.InsertRow(table, row))
	assertStoredRow(t, db, table, row)
}

func TestDatabase_InsertRowConcurrentConflicts(t *testing.T) {
	for _, primary := range []bool{false, true} {
		t.Run(fmt.Sprintf("Primary%t", primary), func(t *testing.T) {
			db, _ := openWriterDatabase(t)
			table, _ := rowInsertFixture()
			require.NoError(t, db.CreateTable(table))
			start := make(chan struct{})
			results := make(chan error, 32)

			var wg sync.WaitGroup

			for id := uint64(1); id <= 32; id++ {
				row := numberedRow(id)

				if primary {
					row[1] = recordpkg.NewUint64(1)
				} else {
					row[2] = recordpkg.NewBytes([]byte("same"))
				}

				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					results <- db.InsertRow(table, row)
				}()
			}

			close(start)
			wg.Wait()
			close(results)
			successes := 0

			for err := range results {
				if err == nil {
					successes++
				} else if primary {
					require.ErrorIs(t, err, ErrDuplicatePrimaryKey)
				} else {
					require.ErrorIs(t, err, ErrUniqueConflict)
				}
			}

			require.Equal(t, 1, successes)
			require.Equal(t, uint64(6), db.Stats().Writes)
			require.Equal(t, uint64(31), db.Stats().WriteErrors)
			require.NoError(t, db.InsertRow(table, numberedRow(1000)))
		})
	}
}

func TestDatabase_InsertRowGroupedConflictsPreserveQueueOrder(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, _ := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	entered, release := blockFirstFlush(t, db)
	first := make(chan error, 1)
	go func() { first <- db.Put([]byte("initial"), nil) }()
	<-entered
	a, b, c := numberedRow(1), numberedRow(2), numberedRow(3)
	b[2] = a[2]
	rows := []schema.Row{a, b, maps.Clone(a), c}
	requests := make([]*writeRequest, 0, len(rows)+1)

	for _, row := range rows {
		request, err := makeRowInsertRequest(table, row)
		require.NoError(t, err)
		requests = append(requests, request)
		db.writes <- request
	}

	raw, err := makeWriteRequest([]Entry{{Key: []byte("last"), Value: []byte("ok")}})
	require.NoError(t, err)
	db.writes <- raw
	release()
	require.NoError(t, waitWriteResult(t, first))
	require.NoError(t, waitWriteResult(t, requests[0].result))
	require.ErrorIs(t, waitWriteResult(t, requests[1].result), ErrUniqueConflict)
	require.ErrorIs(t, waitWriteResult(t, requests[2].result), ErrDuplicatePrimaryKey)
	require.NoError(t, waitWriteResult(t, requests[3].result))
	require.NoError(t, waitWriteResult(t, raw.result))
	require.Equal(t, uint64(3), db.Stats().Syncs)
	require.Equal(t, uint64(12), db.Stats().Writes)
	assertStoredRow(t, db, table, a)
	assertStoredRow(t, db, table, c)
	key, err := schema.EncodePrimaryKey(table, b[1])
	require.NoError(t, err)
	_, found, err := db.Get(key)
	require.NoError(t, err)
	require.False(t, found)
}

func TestDatabase_InsertRowWaitsForSyncAndCloseDrains(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	entered, release := blockFirstFlush(t, db)
	result := make(chan error, 1)
	go func() { result <- db.InsertRow(table, row) }()
	<-entered

	select {
	case err := <-result:
		t.Fatalf("insert returned before sync: %v", err)
	default:
	}

	if db.mu.TryRLock() {
		db.mu.RUnlock()
		t.Fatal("reads must wait until the insert group finishes")
	}

	closed := make(chan error, 1)
	go func() { closed <- db.Close() }()
	release()
	require.NoError(t, waitWriteResult(t, result))
	require.NoError(t, waitWriteResult(t, closed))
	require.ErrorIs(t, db.InsertRow(table, row), ErrClosed)
	reopened, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	assertStoredRow(t, reopened, table, row)
}

func TestDatabase_InsertRowRejectsInvalidPlansBeforeWriting(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, row := rowInsertFixture()
	require.Error(t, db.InsertRow(table, nil))
	row[2] = recordpkg.NewBytes(make([]byte, 3000))
	require.ErrorIs(t, db.InsertRow(table, row), pager.ErrPagerEntryTooLarge)
	require.Zero(t, db.Stats().PageWrites)
	require.Zero(t, db.Stats().Syncs)
	require.Equal(t, uint64(2), db.Stats().WriteErrors)
	assertFileContents(t, path, validDatabaseBytes())
}

func TestDatabase_InsertRowStorageFailureStopsDatabase(t *testing.T) {
	for _, stage := range []string{"Read", "SecondWrite", "Sync"} {
		t.Run(stage, func(t *testing.T) {
			db, _ := openWriterDatabase(t)
			table, row := rowInsertFixture()
			require.NoError(t, db.CreateTable(table))
			cause := errors.New("injected insert storage failure")
			store := db.pager
			hooks := &hookedStore{pageStore: store}

			switch stage {
			case "Read":
				hooks.read = func(uint64) ([]byte, error) { return nil, cause }
			case "SecondWrite":
				writes := 0
				hooks.write = func(id uint64, page []byte) error {
					writes++
					if writes == 2 {
						return cause
					}

					return store.WritePage(id, page)
				}
			case "Sync":
				hooks.sync = func() error { return cause }
			}

			db.pager = hooks
			err := db.InsertRow(table, row)
			require.ErrorIs(t, err, cause)
			require.ErrorIs(t, err, ErrWriteFailed)
			require.Equal(t, uint64(2), db.Stats().Writes)
			require.Equal(t, uint64(1), db.Stats().Syncs)
			before := db.Stats()
			require.ErrorIs(t, db.InsertRow(table, numberedRow(1000)), ErrWriteFailed)
			require.Equal(t, before.PageWrites, db.Stats().PageWrites)
			_, _, err = db.Get([]byte("anything"))
			require.ErrorIs(t, err, ErrWriteFailed)
		})
	}
}

func TestDatabase_RowInsertRequestOwnsInput(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	wantTable, wantRow := cloneRowPlanTable(table), maps.Clone(row)
	request, err := makeRowInsertRequest(table, row)
	require.NoError(t, err)
	row[1] = recordpkg.NewUint64(999)
	row[2] = recordpkg.NewNull()
	table.Indexes[0].ColumnIDs[0] = 1
	db.writes <- request
	require.NoError(t, waitWriteResult(t, request.result))
	assertStoredRow(t, db, wantTable, wantRow)
}

func TestDatabase_InsertRowGroupSyncFailurePreservesRejections(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, _ := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	entered := make(chan struct{})
	release := make(chan struct{})

	var once sync.Once

	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	store := db.pager
	cause := errors.New("insert group sync failed")
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
	go func() { first <- db.Put([]byte("initial"), nil) }()
	<-entered

	var requests []*writeRequest

	for _, id := range []uint64{1, 1, 2} {
		request, err := makeRowInsertRequest(table, numberedRow(id))
		require.NoError(t, err)
		requests = append(requests, request)
		db.writes <- request
	}

	unblock()
	require.NoError(t, waitWriteResult(t, first))

	for i, request := range requests {
		err := waitWriteResult(t, request.result)
		if i == 1 {
			require.ErrorIs(t, err, ErrDuplicatePrimaryKey)
			require.NotErrorIs(t, err, ErrWriteFailed)
		} else {
			require.ErrorIs(t, err, ErrWriteFailed)
			require.ErrorIs(t, err, cause)
		}
	}

	require.Equal(t, uint64(3), db.Stats().Writes)
	require.Equal(t, uint64(2), db.Stats().Syncs)
	require.Equal(t, uint64(1), db.Stats().SyncErrors)
	require.ErrorIs(t, db.InsertRow(table, numberedRow(3)), ErrWriteFailed)
}
