package database

import (
	"errors"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func TestDatabase_GetRowByUniqueIndexReopenAndComposite(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, _ := rowInsertFixture()
	table.Indexes = append(table.Indexes, schema.Index{ID: 100, Name: "by_both", ColumnIDs: []uint32{3, 2}, Unique: true})
	require.NoError(t, db.CreateTable(table))

	for id := uint64(1); id <= 100; id++ {
		require.NoError(t, db.InsertRow(table, numberedRow(id)))
	}

	otherTable := cloneRowPlanTable(table)
	otherTable.ID = 2
	otherRow := numberedRow(3)
	otherRow[1] = recordpkg.NewUint64(999)
	otherTable.Name = "other_users"
	require.NoError(t, db.CreateTable(otherTable))
	require.NoError(t, db.InsertRow(otherTable, otherRow))
	require.Positive(t, db.Stats().SplitOperations)
	require.NoError(t, db.Close())
	reopened, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })

	for id := uint64(1); id <= 100; id++ {
		want := numberedRow(id)
		got, found, err := reopened.GetRowByUniqueIndex(table, 7, want[2])
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, want, got)
		got, found, err = reopened.GetRowByUniqueIndex(table, 100, want[3], want[2])
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, want, got)
	}

	got, found, err := reopened.GetRowByUniqueIndex(otherTable, 7, otherRow[2])
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, otherRow, got)
	clear(got)
	again, found, err := reopened.GetRowByUniqueIndex(otherTable, 7, otherRow[2])
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, otherRow, again)
	got, found, err = reopened.GetRowByUniqueIndex(table, 7, recordpkg.NewBytes([]byte("missing")))
	require.NoError(t, err)
	require.False(t, found)
	require.Nil(t, got)
}

func TestDatabase_GetRowByUniqueIndexInvalidLookup(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, row := rowInsertFixture()

	for _, tc := range []struct {
		indexID uint32
		values  []recordpkg.Value
	}{
		{7, nil},
		{7, []recordpkg.Value{row[2], row[3]}},
		{7, []recordpkg.Value{row[3]}},
		{7, []recordpkg.Value{recordpkg.NewNull()}},
		{9, []recordpkg.Value{row[2]}},
		{0, []recordpkg.Value{row[1]}},
		{999, []recordpkg.Value{row[2]}},
	} {
		got, found, err := db.GetRowByUniqueIndex(table, tc.indexID, tc.values...)
		require.Error(t, err)
		require.False(t, found)
		require.Nil(t, got)
	}

	require.Zero(t, db.Stats().Reads)
	require.Zero(t, db.Stats().PageReads)
}

func TestDatabase_GetRowByUniqueIndexRejectsBadReferences(t *testing.T) {
	for _, scenario := range []string{"Dangling", "MalformedReference", "MalformedRow", "WrongPrimary", "WrongTable", "StaleIndex", "NullIndexedValue"} {
		t.Run(scenario, func(t *testing.T) {
			db, _ := openWriterDatabase(t)
			table, row := rowInsertFixture()
			require.NoError(t, db.CreateTable(table))
			index, err := schema.EncodeIndexEntry(table, 7, row)
			require.NoError(t, err)
			primaryKey := index.Value
			encoded, err := schema.EncodeRow(table, row)
			require.NoError(t, err)

			switch scenario {
			case "MalformedReference":
				index.Value = []byte("garbage")
			case "MalformedRow":
				encoded = []byte{0xff}
			case "WrongPrimary":
				row[1] = recordpkg.NewUint64(999)
				encoded, err = schema.EncodeRow(table, row)
				require.NoError(t, err)
			case "WrongTable":
				other := cloneRowPlanTable(table)
				other.ID = 2
				primaryKey, err = schema.EncodePrimaryKey(other, row[1])
				require.NoError(t, err)
				index.Value = primaryKey
				encoded, err = schema.EncodeRow(other, row)
				require.NoError(t, err)
			case "StaleIndex":
				row[2] = recordpkg.NewBytes([]byte("changed"))
				encoded, err = schema.EncodeRow(table, row)
				require.NoError(t, err)
			case "NullIndexedValue":
				row[2] = recordpkg.NewNull()
				encoded, err = schema.EncodeRow(table, row)
				require.NoError(t, err)
			}

			if scenario != "Dangling" {
				require.NoError(t, db.Put(primaryKey, encoded))
			}

			require.NoError(t, db.Put(index.Key, index.Value))
			got, found, err := db.GetRowByUniqueIndex(table, 7, recordpkg.NewBytes([]byte("Noah")))
			require.Error(t, err)
			require.False(t, found)
			require.Nil(t, got)
			require.NoError(t, db.Put([]byte("still_usable"), nil))
		})
	}
}

func TestDatabase_GetRowByUniqueIndexStorageAndClosedErrors(t *testing.T) {
	for _, stage := range []string{"IndexRead", "PrimaryRead", "Failed", "Closed"} {
		t.Run(stage, func(t *testing.T) {
			db, _ := openWriterDatabase(t)
			table, row := rowInsertFixture()
			require.NoError(t, db.CreateTable(table))
			require.NoError(t, db.InsertRow(table, row))
			cause := errors.New("injected lookup failure")
			want := cause
			store := db.pager

			switch stage {
			case "IndexRead", "PrimaryRead":
				reads := 0
				db.pager = &hookedStore{pageStore: store, read: func(id uint64) ([]byte, error) {
					reads++
					if (stage == "IndexRead" && reads == 3) || (stage == "PrimaryRead" && reads == 4) {
						return nil, cause
					}

					return store.ReadPage(id)
				}}
			case "Failed":
				db.pager = &hookedStore{pageStore: store, sync: func() error { return cause }}
				require.ErrorIs(t, db.Put([]byte("trigger"), nil), ErrWriteFailed)
				want = ErrWriteFailed
			case "Closed":
				require.NoError(t, db.Close())
				want = ErrClosed
			}

			got, found, err := db.GetRowByUniqueIndex(table, 7, row[2])
			require.ErrorIs(t, err, want)
			require.False(t, found)
			require.Nil(t, got)
		})
	}
}

func TestDatabase_GetRowByUniqueIndexHoldsOneReadLock(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	require.NoError(t, db.InsertRow(table, row))
	entered := make(chan struct{})
	release := make(chan struct{})

	var once sync.Once

	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	store := db.pager
	reads := 0
	db.pager = &hookedStore{pageStore: store, read: func(id uint64) ([]byte, error) {
		reads++
		if reads == 1 {
			close(entered)
			<-release
		}

		return store.ReadPage(id)
	}}

	type result struct {
		row   schema.Row
		found bool
		err   error
	}

	read := make(chan result, 1)
	go func() {
		row, found, err := db.GetRowByUniqueIndex(table, 7, row[2])
		read <- result{row, found, err}
	}()
	<-entered

	changed := maps.Clone(row)
	changed[2] = recordpkg.NewBytes([]byte("changed"))
	primaryKey, err := schema.EncodePrimaryKey(table, row[1])
	require.NoError(t, err)
	encoded, err := schema.EncodeRow(table, changed)
	require.NoError(t, err)
	write := make(chan error, 1)
	go func() { write <- db.Put(primaryKey, encoded) }()
	require.Eventually(t, func() bool {
		if db.mu.TryRLock() {
			db.mu.RUnlock()
			return false
		}

		return true
	}, 5*time.Second, time.Millisecond, "writer must be waiting behind the first lookup")
	unblock()
	got := <-read
	require.NoError(t, got.err)
	require.True(t, got.found)
	require.Equal(t, row, got.row)
	require.NoError(t, waitWriteResult(t, write))
	stored, found, err := db.GetRow(table, row[1])
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, changed, stored)
}
