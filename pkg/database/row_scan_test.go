package database

import (
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func indexScanFixture(t testing.TB) (*Database, string, schema.Table) {
	t.Helper()
	db, path := openWriterDatabase(t)
	table, _ := rowInsertFixture()
	table.Indexes = []schema.Index{
		{ID: 9, Name: "by_name", ColumnIDs: []uint32{2}},
		{ID: 11, Name: "by_name_score", ColumnIDs: []uint32{2, 3}, Unique: true},
	}

	require.NoError(t, db.CreateTable(table))

	for id := uint64(1); id <= 100; id++ {
		require.NoError(t, db.InsertRow(table, scanFixtureRow(id)))
	}

	return db, path, table
}

func scanFixtureRow(id uint64) schema.Row {
	row := numberedRow(id)
	values := []recordpkg.Value{recordpkg.NewNull(), recordpkg.NewBytes([]byte("a")), recordpkg.NewBytes([]byte("a\x00")), recordpkg.NewBytes([]byte("ab"))}
	row[2] = values[id%4]
	return row
}

func TestDatabase_ScanRowsByIndexPrefixesAndReopen(t *testing.T) {
	db, path, table := indexScanFixture(t)
	other := cloneRowPlanTable(table)
	other.ID, other.Name = 2, "other_users"
	require.NoError(t, db.CreateTable(other))
	require.NoError(t, db.InsertRow(other, scanFixtureRow(1)))
	require.NoError(t, db.Close())
	reopened, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })

	for _, indexID := range []uint32{9, 11} {
		for _, group := range []uint64{0, 1, 2, 3} {
			value := scanFixtureRow(group)[2]
			rows, err := reopened.ScanRowsByIndex(table, indexID, 100, value)
			require.NoError(t, err)
			require.Len(t, rows, 25)
			position := 0

			for id := uint64(1); id <= 100; id++ {
				if id%4 == group {
					require.Equal(t, scanFixtureRow(id), rows[position])
					position++
				}
			}
		}
	}

	rows, err := reopened.ScanRowsByIndex(table, 11, 10, scanFixtureRow(5)[2], recordpkg.NewInt64(5))
	require.NoError(t, err)
	require.Equal(t, []schema.Row{scanFixtureRow(5)}, rows)
	rows, err = reopened.ScanRowsByIndex(table, 9, 1, scanFixtureRow(1)[2])
	require.NoError(t, err)
	require.Equal(t, []schema.Row{scanFixtureRow(1)}, rows)
	clear(rows[0])
	rows, err = reopened.ScanRowsByIndex(table, 9, 1, scanFixtureRow(1)[2])
	require.NoError(t, err)
	require.Equal(t, []schema.Row{scanFixtureRow(1)}, rows)
	rows, err = reopened.ScanRowsByIndex(table, 9, 100)
	require.NoError(t, err)
	require.Len(t, rows, 100)
	require.Equal(t, scanFixtureRow(4), rows[0])
	require.Equal(t, scanFixtureRow(1), rows[25])
	require.Equal(t, scanFixtureRow(2), rows[50])
	require.Equal(t, scanFixtureRow(3), rows[75])
	rows, err = reopened.ScanRowsByIndex(table, 9, 10, recordpkg.NewBytes(nil))
	require.NoError(t, err)
	require.Nil(t, rows)
}

func TestDatabase_ScanRowsByIndexUniqueNullDuplicates(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	row[2] = recordpkg.NewNull()

	for id := uint64(1); id <= 3; id++ {
		row[1], row[3] = recordpkg.NewUint64(id), recordpkg.NewInt64(int64(id))
		require.NoError(t, db.InsertRow(table, row))
	}

	rows, err := db.ScanRowsByIndex(table, 7, 10, recordpkg.NewNull())
	require.NoError(t, err)
	require.Len(t, rows, 3)

	for i, row := range rows {
		require.Equal(t, recordpkg.NewUint64(uint64(i+1)), row[1])
		require.True(t, row[2].IsNull())
	}
}

func TestDatabase_ScanRowsByIndexRejectsInvalidInput(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, row := rowInsertFixture()

	for _, limit := range []int{0, MaxScanEntries + 1} {
		rows, err := db.ScanRowsByIndex(table, 9, limit)
		require.ErrorIs(t, err, ErrInvalidScan)
		require.Nil(t, rows)
	}

	for _, values := range [][]recordpkg.Value{{row[3]}, {row[2], row[3]}} {
		rows, err := db.ScanRowsByIndex(table, 9, 10, values...)
		require.Error(t, err)
		require.Nil(t, rows)
	}

	require.Zero(t, db.Stats().PageReads)
	require.NoError(t, db.Close())
	rows, err := db.ScanRowsByIndex(table, 9, 10)
	require.ErrorIs(t, err, ErrClosed)
	require.Nil(t, rows)
}

func TestDatabase_ScanRowsByIndexDiscardsInvalidRows(t *testing.T) {
	for _, scenario := range []string{"Dangling", "Stale", "Malformed", "WrongPrimary"} {
		t.Run(scenario, func(t *testing.T) {
			db, _ := openWriterDatabase(t)
			table, _ := rowInsertFixture()
			table.Indexes = table.Indexes[:1]
			require.NoError(t, db.CreateTable(table))
			first, second := numberedRow(1), numberedRow(2)
			first[2], second[2] = recordpkg.NewBytes([]byte("same")), recordpkg.NewBytes([]byte("same"))
			require.NoError(t, db.InsertRow(table, first))
			plan, err := PlanRowInsert(table, second)
			require.NoError(t, err)
			index := plan.Indexes[0]

			switch scenario {
			case "Stale":
				second[2] = recordpkg.NewBytes([]byte("different"))
				plan.Primary.Value, err = schema.EncodeRow(table, second)
				require.NoError(t, err)
			case "Malformed":
				plan.Primary.Value = []byte{0xff}
			case "WrongPrimary":
				second[1] = recordpkg.NewUint64(999)
				plan.Primary.Value, err = schema.EncodeRow(table, second)
				require.NoError(t, err)
			}

			if scenario != "Dangling" {
				require.NoError(t, db.Put(plan.Primary.Key, plan.Primary.Value))
			}

			require.NoError(t, db.Put(index.Key, index.Value))
			rows, err := db.ScanRowsByIndex(table, 9, 10, first[2])
			require.Error(t, err)
			require.Nil(t, rows)
			bound := &KeyBound{Value: first[2], Inclusive: true}
			page, err := db.ScanRowsByIndexRangePage(table, 9, bound, bound, 10, nil)
			require.Error(t, err)
			require.Empty(t, page.Rows)
			require.Nil(t, page.Next)
		})
	}
}

func TestDatabase_ScanRowsByIndexSnapshot(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	require.NoError(t, db.InsertRow(table, row))
	entered, release := make(chan struct{}), make(chan struct{})

	var once sync.Once

	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	store := db.pager
	firstRead := true
	db.pager = &hookedStore{pageStore: store, read: func(id uint64) ([]byte, error) {
		if firstRead {
			firstRead = false
			close(entered)
			<-release
		}

		return store.ReadPage(id)
	}}

	type result struct {
		rows []schema.Row
		err  error
	}

	read := make(chan result, 1)
	go func() {
		rows, err := db.ScanRowsByIndex(table, 9, 10)
		read <- result{rows, err}
	}()
	<-entered
	changed := maps.Clone(row)
	changed[2] = recordpkg.NewBytes([]byte("changed"))
	key, err := schema.EncodePrimaryKey(table, row[1])
	require.NoError(t, err)
	encoded, err := schema.EncodeRow(table, changed)
	require.NoError(t, err)
	write := make(chan error, 1)
	go func() { write <- db.Put(key, encoded) }()
	require.Eventually(t, func() bool {
		if db.mu.TryRLock() {
			db.mu.RUnlock()
			return false
		}

		return true
	}, 5*time.Second, time.Millisecond)
	unblock()
	got := <-read
	require.NoError(t, got.err)
	require.Equal(t, []schema.Row{row}, got.rows)
	require.NoError(t, waitWriteResult(t, write))
}
