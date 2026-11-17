package database

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
)

func TestWriteTxReadModifyWriteConflict(t *testing.T) {
	db, path := openWriterDatabase(t)
	key := []byte("counter")
	require.NoError(t, db.Put(key, []byte{0}))
	first, second := db.BeginWrite(), db.BeginWrite()

	for _, tx := range []*WriteTx{first, second} {
		value, found, err := tx.Get(key)
		require.NoError(t, err)
		require.True(t, found)
		require.NoError(t, tx.Put(key, []byte{value[0] + 1}))
	}

	require.NoError(t, first.Commit())
	before := db.Stats()
	require.ErrorIs(t, second.Commit(), ErrTransactionConflict)
	require.Equal(t, before.PageWrites, db.Stats().PageWrites)
	require.Equal(t, before.Syncs, db.Stats().Syncs)
	value, found, err := db.Get(key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []byte{1}, value)
	retry := db.BeginWrite()
	value, _, err = retry.Get(key)
	require.NoError(t, err)
	require.NoError(t, retry.Put(key, []byte{value[0] + 1}))
	require.NoError(t, retry.Commit())
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestWriteTxReadsOwnRawWritesAndSplits(t *testing.T) {
	db, path := openWriterDatabase(t)
	tx := db.BeginWrite()
	entries := crashEntries()
	require.NoError(t, tx.PutBatch(entries))

	for _, entry := range entries {
		value, found, err := tx.Get(entry.Key)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, entry.Value, value)
		clear(value)
	}

	got, err := tx.Scan(nil, nil, MaxScanEntries)
	require.NoError(t, err)
	require.Equal(t, entries, got)
	clear(got[0].Key)
	clear(got[0].Value)
	require.NoError(t, tx.Delete(entries[0].Key))
	_, found, err := tx.Get(entries[0].Key)
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, tx.Put(entries[0].Key, []byte("replacement")))
	value, found, err := tx.Get(entries[0].Key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []byte("replacement"), value)
	assertFileContents(t, path, validDatabaseBytes())
	assertFileContents(t, path+".wal", []byte{})
	require.Zero(t, db.Stats().PageWrites)
	_, found, err = db.Get(entries[0].Key)
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, tx.Commit())
	value, found, err = db.Get(entries[0].Key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []byte("replacement"), value)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestWriteTxReadsOwnCatalogRowsAndIndexes(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, _ := rowInsertFixture()
	first, second := numberedRow(1), numberedRow(2)
	tx := db.BeginWrite()
	require.NoError(t, tx.CreateTable(table))
	stored, found, err := tx.GetTable(table.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, table, stored)
	stored.Columns[0].Name = "mutated result"
	stored, found, err = tx.GetTableByName(table.Name)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, table, stored)
	require.NoError(t, tx.InsertRow(table, first))
	require.NoError(t, tx.InsertRow(table, second))
	row, found, err := tx.GetRow(table, first[1])
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, first, row)
	clear(row)
	oldName := first[2]
	first[2] = recordpkg.NewBytes([]byte("changed"))
	require.NoError(t, tx.UpdateRow(table, first))
	_, found, err = tx.GetRowByUniqueIndex(table, 7, oldName)
	require.NoError(t, err)
	require.False(t, found)
	row, found, err = tx.GetRowByUniqueIndex(table, 7, first[2])
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, first, row)
	rows, err := tx.ScanRowsByIndex(table, 9, MaxScanEntries)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.NoError(t, tx.DeleteRow(table, second[1]))
	_, found, err = tx.GetRow(table, second[1])
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, tx.InsertRow(table, second))
	_, found, err = tx.GetRow(table, second[1])
	require.NoError(t, err)
	require.True(t, found)
	require.Zero(t, db.Stats().PageWrites)
	require.NoError(t, tx.Commit())
	assertStoredRow(t, db, table, first)
	assertStoredRow(t, db, table, second)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestWriteTxReadConflictsAreSticky(t *testing.T) {
	for _, mode := range []string{"Update", "Delete", "AbsentInsert", "ABA", "Unrelated", "Phantom"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := openWriterDatabase(t)

			key := []byte("key")
			if mode != "AbsentInsert" && mode != "Phantom" {
				require.NoError(t, db.Put(key, []byte("old")))
			}

			tx := db.BeginWrite()
			if mode == "Phantom" {
				entries, err := tx.Scan([]byte("k"), []byte("l"), 10)
				require.NoError(t, err)
				require.Empty(t, entries)
			} else {
				_, _, err := tx.Get(key)
				require.NoError(t, err)
			}

			switch mode {
			case "Delete":
				_, err := db.Delete(key)
				require.NoError(t, err)
			case "ABA":
				require.NoError(t, db.Put(key, []byte("intermediate")))
				require.NoError(t, db.Put(key, []byte("old")))
			case "Unrelated":
				require.NoError(t, db.Put([]byte("elsewhere"), nil))
			default:
				require.NoError(t, db.Put(key, []byte("new")))
			}

			value, found, err := tx.Get(key)
			require.ErrorIs(t, err, ErrTransactionConflict)
			require.Nil(t, value)
			require.False(t, found)
			require.ErrorIs(t, tx.Put([]byte("must-not-write"), nil), ErrTransactionConflict)
			require.ErrorIs(t, tx.Commit(), ErrTransactionConflict)
			require.NoError(t, db.Put([]byte("still-usable"), nil))
		})
	}
}

func TestWriteTxReadOnlyCommitChecksRevisionWithoutSync(t *testing.T) {
	db, _ := openWriterDatabase(t)
	tx := db.BeginWrite()
	_, _, err := tx.Get(nil)
	require.NoError(t, err)
	before := db.Stats()
	require.NoError(t, tx.Commit())
	require.Equal(t, before.Syncs, db.Stats().Syncs)
	require.Equal(t, before.PageWrites, db.Stats().PageWrites)

	tx = db.BeginWrite()
	_, _, err = tx.Get(nil)
	require.NoError(t, err)
	require.NoError(t, db.Put([]byte("other"), nil))
	require.ErrorIs(t, tx.Commit(), ErrTransactionConflict)
}

func TestWriteTxRevisionStartsOnFirstRead(t *testing.T) {
	db, _ := openWriterDatabase(t)
	tx := db.BeginWrite()
	require.NoError(t, tx.Put([]byte("own"), nil))
	require.NoError(t, db.Put([]byte("other"), []byte("latest")))
	value, found, err := tx.Get([]byte("other"))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []byte("latest"), value)
	require.NoError(t, tx.Commit())

	tx = db.BeginWrite()
	require.NoError(t, tx.Put([]byte("blind"), nil))
	require.NoError(t, db.Put([]byte("other"), []byte("changed")))
	require.NoError(t, tx.Commit())
}

func TestWriteTxNoopsAndRejectedTransactionsDoNotInvalidateReads(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	require.NoError(t, db.InsertRow(table, row))
	tx := db.BeginWrite()
	_, _, err := tx.GetRow(table, row[1])
	require.NoError(t, err)
	_, err = db.Delete([]byte("missing"))
	require.NoError(t, err)
	_, err = db.UpdateRow(table, row)
	require.NoError(t, err)
	rejected := db.BeginWrite()
	require.NoError(t, rejected.Put([]byte("discarded"), nil))
	require.NoError(t, rejected.InsertRow(table, row))
	require.ErrorIs(t, rejected.Commit(), ErrDuplicatePrimaryKey)
	require.NoError(t, tx.Commit())
}

func TestWriteTxReadPreviewRejectionAndRollback(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, row := rowInsertFixture()
	tx := db.BeginWrite()
	require.NoError(t, tx.CreateTable(table))
	require.NoError(t, tx.InsertRow(table, row))
	require.NoError(t, tx.InsertRow(table, row))
	_, _, err := tx.Get(nil)
	require.ErrorIs(t, err, ErrDuplicatePrimaryKey)
	require.ErrorIs(t, tx.Commit(), ErrDuplicatePrimaryKey)
	assertFileContents(t, path, validDatabaseBytes())

	tx = db.BeginWrite()
	require.NoError(t, tx.PutBatch(crashEntries()))
	_, _, err = tx.Get(crashEntries()[0].Key)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	_, _, err = tx.Get(nil)
	require.ErrorIs(t, err, ErrTransactionClosed)
	assertFileContents(t, path, validDatabaseBytes())
}

func TestWriteTxReadsAfterCloseAndFailure(t *testing.T) {
	for _, mode := range []string{"Closed", "WriteFailed", "ReadFailed", "InvalidRead"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := openWriterDatabase(t)
			tx := db.BeginWrite()
			_, _, err := tx.Get(nil)
			require.NoError(t, err)
			want := ErrClosed

			switch mode {
			case "Closed":
				require.NoError(t, db.Close())
			case "WriteFailed":
				want = ErrWriteFailed
				db.pager = &hookedStore{pageStore: db.pager, sync: func() error { return errors.New("sync failed") }}
				require.ErrorIs(t, db.Put([]byte("x"), nil), want)
			case "ReadFailed":
				want = errors.New("read failed")
				stage := tx.view.pager.(*stagedPages)
				stage.pageStore = &hookedStore{pageStore: stage.pageStore, read: func(uint64) ([]byte, error) { return nil, want }}
			case "InvalidRead":
				want = ErrInvalidScan
				_, err := tx.Scan(nil, nil, 0)
				require.ErrorIs(t, err, want)
			}

			_, _, err = tx.Get(nil)
			require.ErrorIs(t, err, want)
			require.ErrorIs(t, tx.Commit(), want)
		})
	}
}

func TestWriteTxQueuedCommitValidatesAfterEarlierWrite(t *testing.T) {
	db, _ := openWriterDatabase(t)
	tx := db.BeginWrite()
	_, _, err := tx.Get([]byte("key"))
	require.NoError(t, err)
	require.NoError(t, tx.Put([]byte("discarded"), nil))
	entered, release := blockFirstFlush(t, db)
	writeResult := make(chan error, 1)
	go func() { writeResult <- db.Put([]byte("key"), nil) }()
	<-entered
	commitResult := make(chan error, 1)
	go func() { commitResult <- tx.Commit() }()
	release()
	require.NoError(t, waitWriteResult(t, writeResult))
	require.ErrorIs(t, waitWriteResult(t, commitResult), ErrTransactionConflict)
	_, found, err := db.Get([]byte("discarded"))
	require.NoError(t, err)
	require.False(t, found)
}

func TestWriteTxConcurrentIncrementsWithRetry(t *testing.T) {
	db, path := openWriterDatabase(t)
	key := []byte("counter")
	require.NoError(t, db.Put(key, make([]byte, 8)))
	results := make(chan error, 16)
	start := make(chan struct{})

	var ready, workers sync.WaitGroup

	ready.Add(16)

	for range 16 {
		workers.Go(func() {
			for attempt := range 100 {
				tx := db.BeginWrite()
				value, found, err := tx.Get(key)

				if attempt == 0 {
					ready.Done()
					<-start
				}

				if err != nil || !found {
					results <- fmt.Errorf("read counter: found=%t err=%v", found, err)
					return
				}

				binary.LittleEndian.PutUint64(value, binary.LittleEndian.Uint64(value)+1)

				if err := tx.Put(key, value); err != nil {
					results <- err
					return
				}

				err = tx.Commit()
				if errors.Is(err, ErrTransactionConflict) {
					continue
				}

				results <- err
				return
			}

			results <- errors.New("retry limit reached")
		})
	}

	ready.Wait()
	close(start)
	workers.Wait()

	for range 16 {
		require.NoError(t, <-results)
	}

	value, found, err := db.Get(key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint64(16), binary.LittleEndian.Uint64(value))
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
	info, err := os.Stat(path + ".wal")
	require.NoError(t, err)
	require.Zero(t, info.Size())
}
