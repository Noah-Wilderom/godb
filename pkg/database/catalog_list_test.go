package database

import (
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListTablesPaginationAndOwnership(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, _ := rowInsertFixture()

	names := []string{"z", "a", "a\x00", "a\x00b", "日本語"}
	for i, name := range names {
		table.ID, table.Name = uint32(i+1), name
		require.NoError(t, db.CreateTable(table))
	}

	require.NoError(t, db.Put([]byte("unrelated"), []byte("value")))
	require.NoError(t, db.Close())
	db, err := Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()

	var got []string

	after := ""
	for {
		page, err := db.ListTables(after, 2)
		require.NoError(t, err)

		if len(page) == 0 {
			break
		}

		for _, table := range page {
			got = append(got, table.Name)
		}

		after = page[len(page)-1].Name
		page[0].Columns[0].Name = "mutated"
		page[0].Indexes[0].ColumnIDs[0] = 999
	}

	slices.Sort(names)
	require.Equal(t, names, got)
	original, found, err := db.GetTableByName("a")
	require.NoError(t, err)
	require.True(t, found)
	require.NotEqual(t, "mutated", original.Columns[0].Name)
	require.NotEqual(t, uint32(999), original.Indexes[0].ColumnIDs[0])
	_, err = db.ListTables("", 0)
	require.ErrorIs(t, err, ErrInvalidScan)
	_, err = db.ListTables("", MaxScanEntries+1)
	require.ErrorIs(t, err, ErrInvalidScan)
	_, err = db.ListTables("\xff", 1)
	require.ErrorIs(t, err, ErrInvalidScan)
}

func TestListTablesRejectsCorruptReferences(t *testing.T) {
	for _, reference := range [][]byte{{1}, catalogIDKey(999), catalogIDKey(1)} {
		t.Run(fmt.Sprintf("%x", reference), func(t *testing.T) {
			db, _ := openWriterDatabase(t)
			table, _ := rowInsertFixture()
			table.ID = 1
			require.NoError(t, db.CreateTable(table))
			require.NoError(t, db.Put(catalogNameKey("broken"), reference))
			page, err := db.ListTables("", 10)
			require.ErrorIs(t, err, ErrInvalidCatalog)
			require.Nil(t, page)
		})
	}
}

func BenchmarkDatabaseListTables(b *testing.B) {
	db, _ := openWriterDatabase(b)

	table, _ := rowInsertFixture()
	for i := uint32(1); i <= 100; i++ {
		table.ID, table.Name = i, fmt.Sprintf("table_%03d", i)
		require.NoError(b, db.CreateTable(table))
	}

	b.ReportAllocs()

	for b.Loop() {
		_, err := db.ListTables("", 100)
		if err != nil {
			b.Fatal(err)
		}
	}
}
