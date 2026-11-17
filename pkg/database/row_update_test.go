package database

import (
	"bytes"
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

func TestDatabase_UpdateRowIndexesAndReopen(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, _ := rowInsertFixture()
	table.Indexes = append(table.Indexes, schema.Index{ID: 100, Name: "composite", ColumnIDs: []uint32{2, 3}, Unique: true})
	require.NoError(t, db.CreateTable(table))

	for id := uint64(1); id <= 100; id++ {
		require.NoError(t, db.InsertRow(table, numberedRow(id)))
	}

	for id := uint64(1); id <= 100; id++ {
		previous := numberedRow(id)
		row := maps.Clone(previous)
		row[2] = recordpkg.NewNull()
		row[3] = recordpkg.NewInt64(int64(id + 1000))
		matched, err := db.UpdateRow(table, row)
		require.NoError(t, err)
		require.True(t, matched)
		_, found, err := db.GetRowByUniqueIndex(table, 7, previous[2])
		require.NoError(t, err)
		require.False(t, found)
		assertStoredRow(t, db, table, row)
	}

	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rows, err := db.ScanRowsByIndex(table, 7, MaxScanEntries, recordpkg.NewNull())
	require.NoError(t, err)
	require.Len(t, rows, 100)
	matched, err := db.UpdateRow(table, numberedRow(1))
	require.NoError(t, err)
	require.True(t, matched)
	assertStoredRow(t, db, table, numberedRow(1))
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestDatabase_UpdateRowMissingNoopAndUnindexedColumn(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, row := rowInsertFixture()
	table.Columns = append(table.Columns, schema.Column{ID: 100, Name: "extra", Type: recordpkg.TypeBool})
	row[100] = recordpkg.NewBool(false)
	require.NoError(t, db.CreateTable(table))
	before := db.Stats()
	matched, err := db.UpdateRow(table, row)
	require.NoError(t, err)
	require.False(t, matched)
	require.Equal(t, before.Syncs, db.Stats().Syncs)
	require.Equal(t, before.PageWrites, db.Stats().PageWrites)
	require.NoError(t, db.InsertRow(table, row))
	before = db.Stats()
	matched, err = db.UpdateRow(table, row)
	require.NoError(t, err)
	require.True(t, matched)
	require.Equal(t, before.Syncs, db.Stats().Syncs)
	require.Equal(t, before.PageWrites, db.Stats().PageWrites)
	row[100] = recordpkg.NewBool(true)
	matched, err = db.UpdateRow(table, row)
	require.NoError(t, err)
	require.True(t, matched)
	require.Equal(t, before.Writes+1, db.Stats().Writes)
	require.Equal(t, before.Deletes, db.Stats().Deletes)
	require.Equal(t, before.PageWrites+1, db.Stats().PageWrites)
	assertStoredRow(t, db, table, row)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestDatabase_UpdateRowUniqueConflictWritesNothing(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, _ := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	first, second := numberedRow(1), numberedRow(2)
	require.NoError(t, db.InsertRow(table, first))
	require.NoError(t, db.InsertRow(table, second))
	changed := maps.Clone(first)
	changed[2] = second[2]
	changed[3] = recordpkg.NewInt64(999)
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	before := db.Stats()
	matched, err := db.UpdateRow(table, changed)
	require.ErrorIs(t, err, ErrUniqueConflict)
	require.False(t, matched)
	require.Equal(t, before.PageWrites, db.Stats().PageWrites)
	require.Equal(t, before.Syncs, db.Stats().Syncs)
	assertFileContents(t, path, contents)
	assertStoredRow(t, db, table, first)
	assertStoredRow(t, db, table, second)
	changed[2] = recordpkg.NewBytes([]byte("new"))
	matched, err = db.UpdateRow(table, changed)
	require.NoError(t, err)
	require.True(t, matched)
}

func TestDatabase_UpdateRowQueuedOrderAndOwnership(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	entered, release := blockFirstFlush(t, db)
	first := make(chan error, 1)
	go func() { first <- db.Put([]byte("initial"), nil) }()
	<-entered
	insert, err := makeRowInsertRequest(table, row)
	require.NoError(t, err)
	row[2] = recordpkg.NewBytes([]byte("changed"))
	update, err := makeRowUpdateRequest(table, row)
	require.NoError(t, err)
	row[2] = recordpkg.NewNull()
	secondUpdate, err := makeRowUpdateRequest(table, row)
	require.NoError(t, err)
	del, err := makeRowDeleteRequest(table, row[1])
	require.NoError(t, err)
	missing, err := makeRowUpdateRequest(table, row)
	require.NoError(t, err)
	clear(row)
	table.Indexes[0].ColumnIDs[0] = 1

	for _, request := range []*writeRequest{insert, update, secondUpdate, del, missing} {
		db.writes <- request
	}

	release()
	require.NoError(t, waitWriteResult(t, first))

	for _, request := range []*writeRequest{insert, update, secondUpdate, del, missing} {
		require.NoError(t, waitWriteResult(t, request.result))
	}

	require.True(t, update.matched)
	require.True(t, secondUpdate.matched)
	require.False(t, missing.matched)
	require.Equal(t, uint64(3), db.Stats().Syncs)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestDatabase_UpdateRowConcurrentUniqueWinner(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, _ := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))

	for id := uint64(1); id <= 16; id++ {
		require.NoError(t, db.InsertRow(table, numberedRow(id)))
	}

	start := make(chan struct{})
	results := make(chan error, 16)

	var wg sync.WaitGroup

	for id := uint64(1); id <= 16; id++ {
		wg.Go(func() {
			<-start
			row := numberedRow(id)
			row[2] = recordpkg.NewBytes([]byte("shared"))
			_, err := db.UpdateRow(table, row)
			results <- err
		})
	}

	close(start)
	wg.Wait()
	winners := 0

	for range 16 {
		err := <-results
		if err == nil {
			winners++
		} else {
			require.ErrorIs(t, err, ErrUniqueConflict)
		}
	}

	require.Equal(t, 1, winners)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestDatabase_UpdateRowCorruptionAndStorageFailures(t *testing.T) {
	for _, stage := range []string{"MissingIndex", "MalformedRow", "Read", "SecondWrite", "Sync"} {
		t.Run(stage, func(t *testing.T) {
			db, _ := openWriterDatabase(t)
			table, row := rowInsertFixture()
			require.NoError(t, db.CreateTable(table))
			require.NoError(t, db.InsertRow(table, row))
			plan, err := PlanRowInsert(table, row)
			require.NoError(t, err)
			cause := errors.New("update storage failure")
			store := db.pager
			hooks := &hookedStore{pageStore: store}

			switch stage {
			case "MissingIndex":
				_, err := db.Delete(plan.Indexes[len(plan.Indexes)-1].Key)
				require.NoError(t, err)
				cause = ErrInvalidData
			case "MalformedRow":
				require.NoError(t, db.Put(plan.Primary.Key, []byte{0xff}))
				cause = ErrInvalidData
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
			before := db.Stats()
			row[2] = recordpkg.NewBytes([]byte("new"))
			matched, err := db.UpdateRow(table, row)
			require.ErrorIs(t, err, cause)
			require.ErrorIs(t, err, ErrWriteFailed)
			require.False(t, matched)
			require.Equal(t, before.Writes, db.Stats().Writes)
			require.Equal(t, before.Deletes, db.Stats().Deletes)

			if stage == "MissingIndex" || stage == "MalformedRow" {
				require.Equal(t, before.PageWrites, db.Stats().PageWrites)
			}

			require.ErrorIs(t, db.Put([]byte("later"), nil), ErrWriteFailed)
		})
	}
}

func TestDatabase_UpdateRowMutationBudget(t *testing.T) {
	db, path := openWriterDatabase(t)
	table := schema.Table{ID: 1, Name: "t", PrimaryKeyColumnID: 1, Columns: []schema.Column{
		{ID: 1, Name: "id", Type: recordpkg.TypeUint64}, {ID: 2, Name: "v", Type: recordpkg.TypeUint64},
	}}

	for id := uint32(1); id <= 32; id++ {
		table.Indexes = append(table.Indexes, schema.Index{ID: id, Name: fmt.Sprint(id), ColumnIDs: []uint32{2}})
	}

	require.NoError(t, db.CreateTable(table))
	row := schema.Row{1: recordpkg.NewUint64(1), 2: recordpkg.NewUint64(1)}
	require.NoError(t, db.InsertRow(table, row))
	row[2] = recordpkg.NewUint64(2)
	before := db.Stats()
	matched, err := db.UpdateRow(table, row)
	require.ErrorIs(t, err, ErrBatchTooLarge)
	require.False(t, matched)
	require.Equal(t, before.PageWrites, db.Stats().PageWrites)
	require.Equal(t, before.Syncs, db.Stats().Syncs)
	row[2] = recordpkg.NewUint64(1)
	matched, err = db.UpdateRow(table, row)
	require.NoError(t, err)
	require.True(t, matched)

	table.ID, table.Name = 2, "allowed"
	table.Indexes[31].ColumnIDs = []uint32{1}
	require.NoError(t, db.CreateTable(table))
	require.NoError(t, db.InsertRow(table, row))
	row[2] = recordpkg.NewUint64(2)
	before = db.Stats()
	matched, err = db.UpdateRow(table, row)
	require.NoError(t, err)
	require.True(t, matched)
	require.Equal(t, before.Writes+63, db.Stats().Writes)
	require.Equal(t, before.Deletes+31, db.Stats().Deletes)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestDatabase_UpdateRowWaitsForSync(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	require.NoError(t, db.InsertRow(table, row))
	row[2] = recordpkg.NewBytes([]byte("replacement"))
	entered, release := blockFirstFlush(t, db)
	result := make(chan error, 1)
	matched := make(chan bool, 1)
	go func() {
		found, err := db.UpdateRow(table, row)
		matched <- found
		result <- err
	}()
	<-entered

	select {
	case err := <-result:
		t.Fatalf("update returned before sync: %v", err)
	default:
	}

	if db.mu.TryRLock() {
		db.mu.RUnlock()
		t.Fatal("reader could observe a partial update")
	}

	closed := make(chan error, 1)
	go func() { closed <- db.Close() }()
	release()
	require.NoError(t, waitWriteResult(t, result))
	require.True(t, <-matched)
	require.NoError(t, waitWriteResult(t, closed))
	require.NoError(t, Validate(path))
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	assertStoredRow(t, db, table, row)
}

func TestDatabase_UpdateRowGrowthAndInvalidInput(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, _ := rowInsertFixture()
	table.Indexes = nil
	require.NoError(t, db.CreateTable(table))

	for id := uint64(1); id <= 20; id++ {
		require.NoError(t, db.InsertRow(table, numberedRow(id)))
	}

	before := db.Stats().SplitOperations

	for id := uint64(1); id <= 20; id++ {
		row := numberedRow(id)
		row[2] = recordpkg.NewBytes(bytes.Repeat([]byte("x"), 3000))
		matched, err := db.UpdateRow(table, row)
		require.NoError(t, err)
		require.True(t, matched)
	}

	require.Greater(t, db.Stats().SplitOperations, before)
	matched, err := db.UpdateRow(table, nil)
	require.Error(t, err)
	require.False(t, matched)
	row := numberedRow(1)
	row[2] = recordpkg.NewBytes(make([]byte, 4096))
	_, err = db.UpdateRow(table, row)
	require.ErrorIs(t, err, pager.ErrPagerEntryTooLarge)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
	matched, err = db.UpdateRow(table, numberedRow(1))
	require.ErrorIs(t, err, ErrClosed)
	require.False(t, matched)
}
