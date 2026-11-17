package database

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func TestDatabase_DeleteRowPersistsAllIndexes(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, _ := rowInsertFixture()
	table.Indexes = append(table.Indexes, schema.Index{ID: 100, Name: "composite", ColumnIDs: []uint32{2, 3}, Unique: true})
	require.NoError(t, db.CreateTable(table))

	for id := uint64(1); id <= 100; id++ {
		row := numberedRow(id)
		if id%2 == 0 {
			row[2] = recordpkg.NewNull()
		}

		require.NoError(t, db.InsertRow(table, row))
	}

	for id := uint64(1); id <= 50; id++ {
		deleted, err := db.DeleteRow(table, recordpkg.NewUint64(id))
		require.NoError(t, err)
		require.True(t, deleted)
	}

	require.Equal(t, uint64(250), db.Stats().Deletes)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	for id := uint64(1); id <= 100; id++ {
		_, found, err := db.GetRow(table, recordpkg.NewUint64(id))
		require.NoError(t, err)
		require.Equal(t, id > 50, found)
		_, found, err = db.GetRowByUniqueIndex(table, 3, recordpkg.NewInt64(int64(id)))
		require.NoError(t, err)
		require.Equal(t, id > 50, found)
	}

	for _, index := range table.Indexes {
		rows, err := db.ScanRowsByIndex(table, index.ID, MaxScanEntries)
		require.NoError(t, err)
		require.Len(t, rows, 50)
	}

	require.NoError(t, db.InsertRow(table, numberedRow(1)))
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestDatabase_DeleteRowMissingAndNoIndexes(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, row := rowInsertFixture()
	table.Indexes = nil
	require.NoError(t, db.CreateTable(table))
	before := db.Stats()
	deleted, err := db.DeleteRow(table, row[1])
	require.NoError(t, err)
	require.False(t, deleted)
	require.Equal(t, before.Syncs, db.Stats().Syncs)
	require.Equal(t, before.PageWrites, db.Stats().PageWrites)
	require.NoError(t, db.InsertRow(table, row))
	deleted, err = db.DeleteRow(table, row[1])
	require.NoError(t, err)
	require.True(t, deleted)
	require.Equal(t, uint64(1), db.Stats().Deletes)
	deleted, err = db.DeleteRow(table, recordpkg.NewNull())
	require.Error(t, err)
	require.False(t, deleted)
	require.NoError(t, db.Close())
	deleted, err = db.DeleteRow(table, row[1])
	require.ErrorIs(t, err, ErrClosed)
	require.False(t, deleted)
}

func TestDatabase_RowCursorResumesAfterDeletedPosition(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, _ := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))

	for id := uint64(1); id <= 10; id++ {
		require.NoError(t, db.InsertRow(table, numberedRow(id)))
	}

	first, err := db.ScanRowsByIndexPage(table, 9, 3, nil)
	require.NoError(t, err)
	require.NotNil(t, first.Next)
	require.Equal(t, numberedRow(3), first.Rows[2])

	for _, id := range []uint64{3, 4} {
		deleted, err := db.DeleteRow(table, recordpkg.NewUint64(id))
		require.NoError(t, err)
		require.True(t, deleted)
	}

	next, err := db.ScanRowsByIndexPage(table, 9, 3, first.Next)
	require.NoError(t, err)
	require.Equal(t, []schema.Row{numberedRow(5), numberedRow(6), numberedRow(7)}, next.Rows)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestDatabase_DeleteRowUsesCurrentStoredRow(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	entered, release := blockFirstFlush(t, db)
	first := make(chan error, 1)
	go func() { first <- db.Put([]byte("initial"), nil) }()
	<-entered
	insert, err := makeRowInsertRequest(table, row)
	require.NoError(t, err)
	del, err := makeRowDeleteRequest(table, row[1])
	require.NoError(t, err)
	missing, err := makeRowDeleteRequest(table, row[1])
	require.NoError(t, err)
	row[2] = recordpkg.NewBytes([]byte("updated"))
	second, err := makeRowInsertRequest(table, row)
	require.NoError(t, err)
	delAgain, err := makeRowDeleteRequest(table, row[1])
	require.NoError(t, err)
	table.Indexes[0].ColumnIDs[0] = 1

	for _, request := range []*writeRequest{insert, del, missing, second, delAgain} {
		db.writes <- request
	}

	release()
	require.NoError(t, waitWriteResult(t, first))

	for _, request := range []*writeRequest{insert, del, missing, second, delAgain} {
		require.NoError(t, waitWriteResult(t, request.result))
	}

	require.Equal(t, 4, del.deleted)
	require.Zero(t, missing.deleted)
	require.Equal(t, 4, delAgain.deleted)
	require.Equal(t, uint64(3), db.Stats().Syncs)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestDatabase_DeleteRowChecksAllReferencesBeforeWriting(t *testing.T) {
	for _, stage := range []string{"MissingIndex", "WrongReference", "MalformedRow"} {
		t.Run(stage, func(t *testing.T) {
			db, path := openWriterDatabase(t)
			table, row := rowInsertFixture()
			require.NoError(t, db.CreateTable(table))
			require.NoError(t, db.InsertRow(table, row))
			plan, err := PlanRowInsert(table, row)
			require.NoError(t, err)

			switch stage {
			case "MissingIndex":
				_, err = db.Delete(plan.Indexes[len(plan.Indexes)-1].Key)
				require.NoError(t, err)
			case "WrongReference":
				require.NoError(t, db.Put(plan.Indexes[len(plan.Indexes)-1].Key, []byte("other")))
			case "MalformedRow":
				require.NoError(t, db.Put(plan.Primary.Key, []byte{0xff}))
			}

			contents, err := os.ReadFile(path)
			require.NoError(t, err)
			before := db.Stats()
			deleted, err := db.DeleteRow(table, row[1])
			require.False(t, deleted)
			require.ErrorIs(t, err, ErrInvalidData)
			require.ErrorIs(t, err, ErrWriteFailed)
			require.Equal(t, before.PageWrites, db.Stats().PageWrites)
			require.Equal(t, before.Syncs, db.Stats().Syncs)
			require.Equal(t, before.Deletes, db.Stats().Deletes)
			assertFileContents(t, path, contents)
		})
	}
}

func TestDatabase_DeleteRowWaitsForSyncAndClose(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	require.NoError(t, db.InsertRow(table, row))
	entered, release := blockFirstFlush(t, db)
	result := make(chan error, 1)
	found := make(chan bool, 1)
	go func() {
		deleted, err := db.DeleteRow(table, row[1])
		found <- deleted
		result <- err
	}()
	<-entered

	select {
	case err := <-result:
		t.Fatalf("delete returned before sync: %v", err)
	default:
	}

	if db.mu.TryRLock() {
		db.mu.RUnlock()
		t.Fatal("reader could observe a partially committed delete")
	}

	closed := make(chan error, 1)
	go func() { closed <- db.Close() }()
	release()
	require.NoError(t, waitWriteResult(t, result))
	require.True(t, <-found)
	require.NoError(t, waitWriteResult(t, closed))
	require.NoError(t, Validate(path))
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, exists, err := db.GetRow(table, row[1])
	require.NoError(t, err)
	require.False(t, exists)
}

func TestDatabase_DeleteRowPartialFailureStopsHandle(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	require.NoError(t, db.InsertRow(table, row))
	cause := errors.New("second delete write failed")
	store := db.pager
	writes := 0
	db.pager = &hookedStore{pageStore: store, write: func(id uint64, page []byte) error {
		writes++
		if writes == 2 {
			return cause
		}

		return store.WritePage(id, page)
	}}
	deleted, err := db.DeleteRow(table, row[1])
	require.ErrorIs(t, err, cause)
	require.ErrorIs(t, err, ErrWriteFailed)
	require.False(t, deleted)
	require.Zero(t, db.Stats().Deletes)
	_, _, err = db.GetRow(table, row[1])
	require.ErrorIs(t, err, ErrWriteFailed)
}

func TestDatabase_DeleteRowReservesGroupCapacity(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table := schema.Table{ID: 1, Name: "t", Columns: []schema.Column{{ID: 1, Name: "id", Type: recordpkg.TypeUint64}}, PrimaryKeyColumnID: 1}

	for id := uint32(1); id < MaxBatchEntries; id++ {
		table.Indexes = append(table.Indexes, schema.Index{ID: id, Name: fmt.Sprint(id), ColumnIDs: []uint32{1}})
	}

	require.NoError(t, db.CreateTable(table))

	for id := uint64(1); id <= 2; id++ {
		require.NoError(t, db.InsertRow(table, schema.Row{1: recordpkg.NewUint64(id)}))
	}

	entered, release := blockFirstFlush(t, db)
	first := make(chan error, 1)
	go func() { first <- db.Put([]byte("initial"), nil) }()
	<-entered
	before := db.Stats()

	var requests []*writeRequest

	for id := uint64(1); id <= 2; id++ {
		request, err := makeRowDeleteRequest(table, recordpkg.NewUint64(id))
		require.NoError(t, err)
		require.Len(t, request.entries, MaxBatchEntries)
		requests = append(requests, request)
		db.writes <- request
	}

	release()
	require.NoError(t, waitWriteResult(t, first))

	for _, request := range requests {
		require.NoError(t, waitWriteResult(t, request.result))
		require.Equal(t, MaxBatchEntries, request.deleted)
	}

	require.Equal(t, before.Syncs+3, db.Stats().Syncs)
	require.Equal(t, uint64(2*MaxBatchEntries), db.Stats().Deletes)
}
