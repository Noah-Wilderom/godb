package database

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/pager"
	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func TestCatalogFormat(t *testing.T) {
	table := schema.Table{
		ID: 1, Name: "users", PrimaryKeyColumnID: 1,
		Columns: []schema.Column{{ID: 1, Name: "id", Type: recordpkg.TypeUint64}},
	}
	request, err := makeCreateTableRequest(table)
	require.NoError(t, err)
	require.Equal(t, []byte{0, 1, 0, 0, 0, 1}, request.entries[0].Key)
	require.Equal(t, append([]byte{0, 2}, []byte("users")...), request.entries[1].Key)
	require.Equal(t, request.entries[0].Key, request.entries[1].Value)
	want := `{"ID":1,"Name":"users","Columns":[{"ID":1,"Name":"id","Type":14,"Nullable":false}],"PrimaryKeyColumnID":1,"Indexes":null}`
	require.Equal(t, append([]byte{1}, []byte(want)...), request.entries[0].Value)
	decoded, err := decodeCatalogTable(request.entries[0].Value)
	require.NoError(t, err)
	require.Equal(t, table, decoded)
}

func TestDatabase_CatalogBoundaryDefinitions(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, _ := rowInsertFixture()
	table.ID, table.Name, table.Indexes = math.MaxUint32, "日本語\x00table", nil
	require.NoError(t, db.CreateTable(table))
	got, found, err := db.GetTableByName(table.Name)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, table, got)
	_, found, err = db.GetTableByName("日本語")
	require.NoError(t, err)
	require.False(t, found)

	for id := uint32(1); id <= MaxBatchEntries; id++ {
		table.Indexes = append(table.Indexes, schema.Index{ID: id, Name: fmt.Sprintf("index_%d", id), ColumnIDs: []uint32{1}})
	}

	require.ErrorIs(t, db.CreateTable(table), ErrBatchTooLarge)
	table.Indexes = nil
	table.Name = strings.Repeat("n", 4096)
	require.ErrorIs(t, db.CreateTable(table), pager.ErrPagerEntryTooLarge)
	_, _, err = db.GetTableByName(table.Name)
	require.ErrorIs(t, err, pager.ErrPagerEntryTooLarge)
}

func TestDatabase_CatalogPersistsAcrossSplitsAndReopen(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, row := rowInsertFixture()

	for id := uint32(1); id <= 32; id++ {
		table.ID, table.Name = id, fmt.Sprintf("table_%d", id)
		require.NoError(t, db.CreateTable(table))
		require.NoError(t, db.InsertRow(table, row))
	}

	require.Positive(t, db.Stats().SplitOperations)
	require.NoError(t, db.Close())
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	for id := uint32(1); id <= 32; id++ {
		table.ID, table.Name = id, fmt.Sprintf("table_%d", id)
		byID, found, err := db.GetTable(id)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, table, byID)
		byName, found, err := db.GetTableByName(table.Name)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, table, byName)
		stored, found, err := db.GetRow(byName, row[1])
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, row, stored)
		require.ErrorIs(t, db.CreateTable(table), ErrTableIDExists)
	}
}

func TestDatabase_CreateTableConflictsDoNotWrite(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, _ := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	before := db.Stats()
	sameID := cloneRowPlanTable(table)
	sameID.Name = "other"
	require.ErrorIs(t, db.CreateTable(sameID), ErrTableIDExists)
	sameName := cloneRowPlanTable(table)
	sameName.ID = 2
	require.ErrorIs(t, db.CreateTable(sameName), ErrTableNameExists)
	after := db.Stats()
	require.Equal(t, before.PageWrites, after.PageWrites)
	require.Equal(t, before.Syncs, after.Syncs)
	require.Equal(t, before.Writes, after.Writes)
	_, found, err := db.GetTable(2)
	require.NoError(t, err)
	require.False(t, found)
	_, found, err = db.GetTableByName("other")
	require.NoError(t, err)
	require.False(t, found)
	sameName.Name = strings.ToUpper(table.Name)
	require.NoError(t, db.CreateTable(sameName))
}

func TestDatabase_CreateTableConcurrentConflicts(t *testing.T) {
	for _, sameID := range []bool{false, true} {
		t.Run(fmt.Sprintf("SameID%t", sameID), func(t *testing.T) {
			db, _ := openWriterDatabase(t)
			start := make(chan struct{})
			results := make(chan error, 32)

			for id := uint32(1); id <= 32; id++ {
				table, _ := rowInsertFixture()
				if sameID {
					table.Name = fmt.Sprintf("table_%d", id)
				} else {
					table.ID = id
				}

				go func() {
					<-start
					results <- db.CreateTable(table)
				}()
			}

			close(start)
			successes := 0

			for range 32 {
				err := waitWriteResult(t, results)
				if err == nil {
					successes++
				} else if sameID {
					require.ErrorIs(t, err, ErrTableIDExists)
				} else {
					require.ErrorIs(t, err, ErrTableNameExists)
				}
			}

			require.Equal(t, 1, successes)
			require.Equal(t, uint64(2), db.Stats().Writes)
		})
	}
}

func TestDatabase_CreateTableGroupedConflicts(t *testing.T) {
	db, _ := openWriterDatabase(t)
	entered, release := blockFirstFlush(t, db)
	first := make(chan error, 1)
	go func() { first <- db.Put([]byte("initial"), nil) }()
	<-entered
	table, _ := rowInsertFixture()

	var requests []*writeRequest

	for _, id := range []uint32{1, 2, 1, 3} {
		table.ID = id
		if id == 3 {
			table.Name = "other"
		}

		request, err := makeCreateTableRequest(table)
		require.NoError(t, err)
		requests = append(requests, request)
		db.writes <- request
	}

	release()
	require.NoError(t, waitWriteResult(t, first))
	require.NoError(t, waitWriteResult(t, requests[0].result))
	require.ErrorIs(t, waitWriteResult(t, requests[1].result), ErrTableNameExists)
	require.ErrorIs(t, waitWriteResult(t, requests[2].result), ErrTableIDExists)
	require.NoError(t, waitWriteResult(t, requests[3].result))
	require.Equal(t, uint64(2), db.Stats().Syncs)
	require.Equal(t, uint64(5), db.Stats().Writes)
}

func TestDatabase_CatalogOwnsDefinitions(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, _ := rowInsertFixture()
	want := cloneRowPlanTable(table)
	request, err := makeCreateTableRequest(table)
	require.NoError(t, err)
	table.Name = "changed"
	table.Columns[0].Name = "changed"
	table.Indexes[0].ColumnIDs[0] = 1
	db.writes <- request
	require.NoError(t, waitWriteResult(t, request.result))
	got, found, err := db.GetTable(want.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, want, got)
	got.Columns[0].Name = "changed again"
	got.Indexes[0].ColumnIDs[0] = 1
	again, found, err := db.GetTableByName(want.Name)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, want, again)
}

func TestDatabase_CatalogInvalidAndMissing(t *testing.T) {
	db, _ := openWriterDatabase(t)
	require.Error(t, db.CreateTable(schema.Table{}))
	table, _ := rowInsertFixture()
	table.Columns[0].Name = strings.Repeat("x", 4096)
	require.ErrorIs(t, db.CreateTable(table), pager.ErrPagerEntryTooLarge)
	require.Zero(t, db.Stats().PageWrites)
	require.Zero(t, db.Stats().Syncs)

	for _, name := range []string{"", string([]byte{0xff})} {
		_, found, err := db.GetTableByName(name)
		require.Error(t, err)
		require.False(t, found)
	}

	_, found, err := db.GetTable(0)
	require.Error(t, err)
	require.False(t, found)
	_, found, err = db.GetTable(1)
	require.NoError(t, err)
	require.False(t, found)
	_, found, err = db.GetTableByName("missing")
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, db.Close())
	table, _ = rowInsertFixture()
	require.ErrorIs(t, db.CreateTable(table), ErrClosed)
	_, _, err = db.GetTable(1)
	require.ErrorIs(t, err, ErrClosed)
	_, _, err = db.GetTableByName("users")
	require.ErrorIs(t, err, ErrClosed)
}

func TestDatabase_CatalogRejectsCorruption(t *testing.T) {
	table, _ := rowInsertFixture()
	request, err := makeCreateTableRequest(table)
	require.NoError(t, err)
	valid := request.entries[0].Value

	for name, encoded := range map[string][]byte{
		"empty":           nil,
		"version":         {2, '{', '}'},
		"truncated":       valid[:len(valid)-1],
		"invalid schema":  append([]byte{1}, []byte(`{}`)...),
		"trailing":        append(bytes.Clone(valid), 'x'),
		"unknown field":   bytes.Replace(valid, []byte(`"ID":1`), []byte(`"Extra":1,"ID":1`), 1),
		"duplicate field": bytes.Replace(valid, []byte(`"ID":1`), []byte(`"ID":2,"ID":1`), 1),
		"wrong ID":        bytes.Replace(valid, []byte(`"ID":1`), []byte(`"ID":2`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			db, _ := openWriterDatabase(t)
			require.NoError(t, db.CreateTable(table))
			require.NoError(t, db.Put(catalogIDKey(table.ID), encoded))
			got, found, err := db.GetTable(table.ID)
			require.ErrorIs(t, err, ErrInvalidCatalog)
			require.False(t, found)
			require.Equal(t, schema.Table{}, got)
			_, _, err = db.GetTableByName(table.Name)
			require.ErrorIs(t, err, ErrInvalidCatalog)
		})
	}

	for name, ref := range map[string][]byte{
		"short":           {0},
		"wrong namespace": {1, 1, 0, 0, 0, 1},
		"zero":            catalogIDKey(0),
		"dangling":        catalogIDKey(999),
		"wrong name":      catalogIDKey(2),
	} {
		t.Run(name, func(t *testing.T) {
			db, _ := openWriterDatabase(t)
			require.NoError(t, db.CreateTable(table))
			other := cloneRowPlanTable(table)
			other.ID, other.Name = 2, "other"
			require.NoError(t, db.CreateTable(other))
			require.NoError(t, db.Put(catalogNameKey(table.Name), ref))
			_, found, err := db.GetTableByName(table.Name)
			require.ErrorIs(t, err, ErrInvalidCatalog)
			require.False(t, found)
			_, _, err = db.GetTable(table.ID)
			require.ErrorIs(t, err, ErrInvalidCatalog)
		})
	}

	db, _ := openWriterDatabase(t)
	require.NoError(t, db.Put(catalogIDKey(table.ID), valid))
	_, _, err = db.GetTable(table.ID)
	require.ErrorIs(t, err, ErrInvalidCatalog)
}

func TestDatabase_CreateTableStorageFailure(t *testing.T) {
	for _, stage := range []string{"Read", "SecondWrite", "Sync"} {
		t.Run(stage, func(t *testing.T) {
			db, _ := openWriterDatabase(t)
			table, _ := rowInsertFixture()
			cause := errors.New("catalog storage failure")
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
			err := db.CreateTable(table)
			require.ErrorIs(t, err, cause)
			require.ErrorIs(t, err, ErrWriteFailed)
			require.ErrorIs(t, db.CreateTable(table), ErrWriteFailed)
			_, _, err = db.GetTable(table.ID)
			require.ErrorIs(t, err, ErrWriteFailed)
		})
	}
}

func TestDatabase_CreateTableWaitsForSync(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, _ := rowInsertFixture()
	entered, release := blockFirstFlush(t, db)
	result := make(chan error, 1)
	go func() { result <- db.CreateTable(table) }()
	<-entered

	select {
	case err := <-result:
		t.Fatalf("create returned before sync: %v", err)
	default:
	}

	if db.mu.TryRLock() {
		db.mu.RUnlock()
		t.Fatal("catalog readers must wait for the write group")
	}

	release()
	require.NoError(t, waitWriteResult(t, result))
}
