package database

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/pager"
	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
)

func TestWriteTxCatalogAndRowsInOrder(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, _ := rowInsertFixture()
	first, second := numberedRow(1), numberedRow(2)
	tx := db.BeginWrite()
	require.NoError(t, tx.CreateTable(table))
	require.NoError(t, tx.InsertRow(table, first))
	require.NoError(t, tx.InsertRow(table, second))
	first[2] = recordpkg.NewBytes([]byte("changed"))
	require.NoError(t, tx.UpdateRow(table, first))
	require.NoError(t, tx.DeleteRow(table, second[1]))
	_, found, err := db.GetTable(table.ID)
	require.NoError(t, err)
	require.False(t, found)
	require.Zero(t, db.Stats().PageWrites)
	require.NoError(t, tx.Commit())
	require.Equal(t, uint64(1), db.Stats().Syncs)
	require.Equal(t, uint64(1), db.Stats().WriteRequests)
	assertStoredRow(t, db, table, first)
	_, found, err = db.GetRow(table, second[1])
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
	db, err = Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	assertStoredRow(t, db, table, first)
}

func TestWriteTxConflictDiscardsCatalogAndSplits(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, row := rowInsertFixture()
	before := *db.header
	tx := db.BeginWrite()
	require.NoError(t, tx.CreateTable(table))
	require.NoError(t, tx.PutBatch(crashEntries()))
	require.NoError(t, tx.InsertRow(table, row))
	require.NoError(t, tx.InsertRow(table, row))
	require.ErrorIs(t, tx.Commit(), ErrDuplicatePrimaryKey)
	require.Equal(t, before, *db.header)
	require.Greater(t, db.Stats().SplitOperations, uint64(0))
	require.Zero(t, db.Stats().PageWrites)
	require.Zero(t, db.Stats().Writes)
	require.Zero(t, db.Stats().Syncs)
	assertFileContents(t, path, validDatabaseBytes())
	assertFileContents(t, path+".wal", []byte{})
	_, found, err := db.GetTable(table.ID)
	require.NoError(t, err)
	require.False(t, found)
	_, found, err = db.Get(crashEntries()[0].Key)
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, db.PutBatch(crashEntries()))
	require.NoError(t, db.CreateTable(table))
	require.NoError(t, db.InsertRow(table, row))
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestWriteTxUniqueConflictRestoresDeletedRow(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, _ := rowInsertFixture()
	first, second := numberedRow(1), numberedRow(2)
	require.NoError(t, db.CreateTable(table))
	require.NoError(t, db.InsertRow(table, first))
	require.NoError(t, db.InsertRow(table, second))
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	before := db.Stats()
	tx := db.BeginWrite()
	require.NoError(t, tx.DeleteRow(table, first[1]))
	conflicting := numberedRow(3)
	conflicting[2] = second[2]
	require.NoError(t, tx.InsertRow(table, conflicting))
	require.ErrorIs(t, tx.Commit(), ErrUniqueConflict)
	assertFileContents(t, path, contents)
	require.Equal(t, before.Writes, db.Stats().Writes)
	require.Equal(t, before.Deletes, db.Stats().Deletes)
	require.Equal(t, before.PageWrites, db.Stats().PageWrites)
	assertStoredRow(t, db, table, first)
	assertStoredRow(t, db, table, second)

	tx = db.BeginWrite()
	require.NoError(t, tx.DeleteRow(table, first[1]))
	conflicting[2], conflicting[3] = first[2], first[3]
	require.NoError(t, tx.InsertRow(table, conflicting))
	require.NoError(t, tx.Commit())
	assertStoredRow(t, db, table, conflicting)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestWriteTxStateAndValidationErrors(t *testing.T) {
	db, _ := openWriterDatabase(t)
	tx := db.BeginWrite()
	require.NoError(t, tx.Put([]byte("a"), nil))
	require.ErrorIs(t, tx.Put([]byte("oversized"), make([]byte, 4096)), pager.ErrPagerEntryTooLarge)
	require.ErrorIs(t, tx.Delete([]byte("a")), pager.ErrPagerEntryTooLarge)
	require.ErrorIs(t, tx.Commit(), pager.ErrPagerEntryTooLarge)
	require.ErrorIs(t, tx.Commit(), ErrTransactionClosed)
	require.ErrorIs(t, tx.Rollback(), ErrTransactionClosed)
	require.ErrorIs(t, tx.Put(nil, nil), ErrTransactionClosed)
	_, found, err := db.Get([]byte("a"))
	require.NoError(t, err)
	require.False(t, found)

	tx = db.BeginWrite()
	require.NoError(t, tx.Put([]byte("rolled-back"), nil))
	require.NoError(t, tx.Rollback())
	require.ErrorIs(t, tx.Commit(), ErrTransactionClosed)
	require.ErrorIs(t, tx.Rollback(), ErrTransactionClosed)
	require.NoError(t, db.BeginWrite().Commit())
	require.Zero(t, db.Stats().Syncs)

	var zero WriteTx

	require.ErrorIs(t, zero.Commit(), ErrTransactionClosed)
	require.ErrorIs(t, zero.Put(nil, nil), ErrTransactionClosed)
	require.ErrorIs(t, zero.Rollback(), ErrTransactionClosed)
	tx = db.BeginWrite()
	require.NoError(t, tx.Put([]byte("closed"), nil))
	require.NoError(t, db.Close())
	require.ErrorIs(t, tx.Commit(), ErrClosed)
	require.ErrorIs(t, db.BeginWrite().Commit(), ErrClosed)
}

func TestWriteTxBudgetAndOwnedInputs(t *testing.T) {
	db, path := openWriterDatabase(t)
	tx := db.BeginWrite()
	key, value := []byte("original"), []byte("value")
	require.NoError(t, tx.Put(key, value))
	clear(key)
	clear(value)

	for i := 1; i < MaxBatchEntries; i++ {
		require.NoError(t, tx.Put(fmt.Appendf(nil, "key%03d", i), nil))
	}

	require.NoError(t, tx.Commit())
	stored, found, err := db.Get([]byte("original"))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []byte("value"), stored)
	tx = db.BeginWrite()

	for range MaxBatchEntries {
		require.NoError(t, tx.Delete([]byte("original")))
	}

	require.ErrorIs(t, tx.Put(nil, nil), ErrBatchTooLarge)
	require.ErrorIs(t, tx.Commit(), ErrBatchTooLarge)
	_, found, err = db.Get([]byte("original"))
	require.NoError(t, err)
	require.True(t, found)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestWriteTxRawOperationOrder(t *testing.T) {
	db, _ := openWriterDatabase(t)
	tx := db.BeginWrite()
	require.NoError(t, tx.Put([]byte("a"), []byte("old")))
	require.NoError(t, tx.DeleteBatch([][]byte{[]byte("a"), []byte("missing")}))
	require.NoError(t, tx.Put([]byte("a"), []byte("new")))
	require.NoError(t, tx.PutBatch(nil))
	require.NoError(t, tx.DeleteBatch(nil))
	require.NoError(t, tx.Commit())
	value, found, err := db.Get([]byte("a"))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []byte("new"), value)
	require.Equal(t, uint64(1), db.Stats().Deletes)
}

func TestWriteTxCatalogRejectionsDoNotPoisonDatabase(t *testing.T) {
	for _, mode := range []string{"DuplicateTable", "MissingTable", "SchemaMismatch"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := openWriterDatabase(t)
			table, row := rowInsertFixture()
			require.NoError(t, db.CreateTable(table))
			tx := db.BeginWrite()
			require.NoError(t, tx.Put([]byte("discarded"), nil))
			want := ErrTableIDExists

			switch mode {
			case "DuplicateTable":
				require.NoError(t, tx.CreateTable(table))
			case "MissingTable":
				table.ID = 2
				want = ErrTableNotFound
				require.NoError(t, tx.InsertRow(table, row))
			case "SchemaMismatch":
				table.Name = "changed"
				want = ErrSchemaMismatch
				require.NoError(t, tx.InsertRow(table, row))
			}

			require.ErrorIs(t, tx.Commit(), want)
			_, found, err := db.Get([]byte("discarded"))
			require.NoError(t, err)
			require.False(t, found)
			require.NoError(t, db.Put([]byte("later"), nil))
		})
	}
}

func TestWriteTxOwnsSchemaAndRow(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, row := rowInsertFixture()
	wantTable := cloneTableDefinition(table)
	_, wantRow := rowInsertFixture()
	tx := db.BeginWrite()
	require.NoError(t, tx.CreateTable(table))
	require.NoError(t, tx.InsertRow(table, row))
	clear(row)
	table.Columns[0].Name = "changed"
	table.Indexes[0].ColumnIDs[0] = 1
	require.NoError(t, tx.Commit())
	assertStoredRow(t, db, wantTable, wantRow)
}

func TestWriteTxNoopAndUpdateBudget(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	require.NoError(t, db.InsertRow(table, row))
	before := db.Stats()
	tx := db.BeginWrite()
	require.NoError(t, tx.UpdateRow(table, row))
	require.NoError(t, tx.UpdateRow(table, numberedRow(99)))
	require.NoError(t, tx.DeleteRow(table, numberedRow(99)[1]))
	require.NoError(t, tx.Commit())
	require.Equal(t, before.Syncs, db.Stats().Syncs)
	require.Equal(t, before.PageWrites, db.Stats().PageWrites)

	tx = db.BeginWrite()
	for range MaxBatchEntries / (1 + 2*len(table.Indexes)) {
		require.NoError(t, tx.UpdateRow(table, row))
	}

	require.ErrorIs(t, tx.UpdateRow(table, row), ErrBatchTooLarge)
	require.ErrorIs(t, tx.Commit(), ErrBatchTooLarge)
	require.Equal(t, before.PageWrites, db.Stats().PageWrites)
}

func TestWriteTxConcurrentCommitOnlyOnce(t *testing.T) {
	db, _ := openWriterDatabase(t)
	tx := db.BeginWrite()
	results := make(chan error, 16)

	var wg sync.WaitGroup

	for i := range 16 {
		wg.Go(func() { results <- tx.Put(fmt.Appendf(nil, "key%02d", i), nil) })
	}

	wg.Wait()

	for range 16 {
		require.NoError(t, <-results)
	}

	for range 2 {
		wg.Go(func() { results <- tx.Commit() })
	}

	wg.Wait()

	first, second := <-results, <-results
	if first != nil {
		first, second = second, first
	}

	require.NoError(t, first)
	require.ErrorIs(t, second, ErrTransactionClosed)
	require.Equal(t, uint64(1), db.Stats().Syncs)
	require.Equal(t, uint64(16), db.Stats().Writes)
}

func TestWriteTxDoesNotAbsorbNeighboringRequests(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	require.NoError(t, db.InsertRow(table, row))
	entered, release := blockFirstFlush(t, db)
	initial := make(chan error, 1)
	go func() { initial <- db.Put([]byte("initial"), nil) }()
	<-entered
	before, err := makeWriteRequest([]Entry{{Key: []byte("before")}})
	require.NoError(t, err)
	after, err := makeWriteRequest([]Entry{{Key: []byte("after")}})
	require.NoError(t, err)
	tx := db.BeginWrite()
	require.NoError(t, tx.Put([]byte("discarded"), nil))
	require.NoError(t, tx.InsertRow(table, row))
	request := &writeRequest{transaction: tx.requests, result: make(chan error, 1)}
	db.writes <- before
	db.writes <- request
	db.writes <- after
	release()
	require.NoError(t, waitWriteResult(t, initial))
	require.NoError(t, waitWriteResult(t, before.result))
	require.ErrorIs(t, waitWriteResult(t, request.result), ErrDuplicatePrimaryKey)
	require.NoError(t, waitWriteResult(t, after.result))

	for _, key := range []string{"initial", "before", "after", "discarded"} {
		_, found, err := db.Get([]byte(key))
		require.NoError(t, err)
		require.Equal(t, key != "discarded", found)
	}

	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestWriteTxConcurrentUniqueWinner(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, _ := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	results := make(chan error, 16)
	start := make(chan struct{})

	var wg sync.WaitGroup

	for id := uint64(1); id <= 16; id++ {
		wg.Go(func() {
			<-start

			tx := db.BeginWrite()
			if err := tx.Put(fmt.Appendf(nil, "marker%02d", id), nil); err != nil {
				results <- err
				return
			}

			row := numberedRow(id)

			row[2] = recordpkg.NewBytes([]byte("shared"))
			if err := tx.InsertRow(table, row); err != nil {
				results <- err
				return
			}

			results <- tx.Commit()
		})
	}

	close(start)
	wg.Wait()
	winners, markers := 0, 0

	for id := uint64(1); id <= 16; id++ {
		if err := <-results; err == nil {
			winners++
		} else {
			require.ErrorIs(t, err, ErrUniqueConflict)
		}

		_, found, err := db.Get(fmt.Appendf(nil, "marker%02d", id))
		require.NoError(t, err)

		if found {
			markers++
		}
	}

	require.Equal(t, 1, winners)
	require.Equal(t, 1, markers)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestWriteTxWaitsForSyncAndClose(t *testing.T) {
	db, path := openWriterDatabase(t)
	tx := db.BeginWrite()
	require.NoError(t, tx.Put([]byte("a"), nil))
	require.NoError(t, tx.Put([]byte("b"), nil))
	entered, release := blockFirstFlush(t, db)
	result := make(chan error, 1)
	go func() { result <- tx.Commit() }()
	<-entered

	select {
	case err := <-result:
		t.Fatalf("commit returned before sync: %v", err)
	default:
	}

	if db.mu.TryRLock() {
		db.mu.RUnlock()
		t.Fatal("reader could observe a partial transaction")
	}

	require.ErrorIs(t, tx.Rollback(), ErrTransactionClosed)
	closed := make(chan error, 1)
	go func() { closed <- db.Close() }()
	release()
	require.NoError(t, waitWriteResult(t, result))
	require.NoError(t, waitWriteResult(t, closed))
	require.NoError(t, Validate(path))
}

func TestWriteTxPreparationFailureWritesNothing(t *testing.T) {
	db, path := openWriterDatabase(t)
	require.NoError(t, db.PutBatch(crashEntries()))
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	before := db.Stats()
	cause := errors.New("read interrupted")
	store := db.pager
	reads := 0
	db.pager = &hookedStore{pageStore: store, read: func(id uint64) ([]byte, error) {
		reads++
		if reads == 3 {
			return nil, cause
		}

		return store.ReadPage(id)
	}}
	tx := db.BeginWrite()
	require.NoError(t, tx.Put([]byte("a"), nil))
	require.NoError(t, tx.Put([]byte("z"), nil))
	require.ErrorIs(t, tx.Commit(), cause)
	require.Equal(t, before.PageWrites, db.Stats().PageWrites)
	assertFileContents(t, path, contents)
	require.ErrorIs(t, db.Put(nil, nil), ErrWriteFailed)
}
