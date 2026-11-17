package database

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func TestDatabase_ScanRowsByIndexPageDuplicatesAndPrefixes(t *testing.T) {
	db, _, table := indexScanFixture(t)

	for _, id := range []uint32{9, 11} {
		for _, values := range [][]recordpkg.Value{nil, {scanFixtureRow(1)[2]}, {recordpkg.NewNull()}, {scanFixtureRow(2)[2]}} {
			want, err := db.ScanRowsByIndex(table, id, MaxScanEntries, values...)
			require.NoError(t, err)

			var (
				cursor *ScanCursor
				got    []schema.Row
			)

			for pageNumber := range 100 {
				page, err := db.ScanRowsByIndexPage(table, id, 7, cursor, values...)
				require.NoError(t, err)
				got = append(got, page.Rows...)

				if page.Next == nil {
					break
				}

				require.Less(t, pageNumber, 99)
				cursor = page.Next
			}

			require.Equal(t, want, got)
		}
	}

	page, err := db.ScanRowsByIndexPage(table, 11, 1, nil, scanFixtureRow(1)[2], recordpkg.NewInt64(1))
	require.NoError(t, err)
	require.Equal(t, []schema.Row{scanFixtureRow(1)}, page.Rows)
	require.Nil(t, page.Next)
}

func TestDatabase_ScanRowsByIndexPageUniqueNullDuplicates(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	row[2] = recordpkg.NewNull()

	for id := uint64(1); id <= 3; id++ {
		row[1], row[3] = recordpkg.NewUint64(id), recordpkg.NewInt64(int64(id))
		require.NoError(t, db.InsertRow(table, row))
	}

	var cursor *ScanCursor

	for id := uint64(1); id <= 3; id++ {
		page, err := db.ScanRowsByIndexPage(table, 7, 1, cursor, recordpkg.NewNull())
		require.NoError(t, err)
		require.Len(t, page.Rows, 1)
		require.Equal(t, recordpkg.NewUint64(id), page.Rows[0][1])
		cursor = page.Next
	}

	require.Nil(t, cursor)
}

func TestDatabase_ScanRowsByIndexPageCursorScope(t *testing.T) {
	db, _, table := indexScanFixture(t)
	first, err := db.ScanRowsByIndexPage(table, 11, 1, nil)
	require.NoError(t, err)
	require.NotNil(t, first.Next)

	for _, change := range []func(*schema.Table){
		func(table *schema.Table) { table.ID = 2 },
		func(table *schema.Table) { table.PrimaryKeyColumnID = 3 },
		func(table *schema.Table) { table.Indexes[1].Unique = false },
		func(table *schema.Table) { slices.Reverse(table.Indexes[1].ColumnIDs) },
		func(table *schema.Table) { table.Columns[1].Type = recordpkg.TypeText },
		func(table *schema.Table) { table.Columns[1].Nullable = false },
		func(table *schema.Table) {
			table.Columns = append(table.Columns, schema.Column{ID: 99, Name: "extra", Type: recordpkg.TypeBool})
		},
	} {
		changed := cloneRowPlanTable(table)
		change(&changed)
		page, err := db.ScanRowsByIndexPage(changed, 11, 1, first.Next)
		require.ErrorIs(t, err, ErrInvalidCursor)
		require.Equal(t, RowPage{}, page)
	}

	page, err := db.ScanRowsByIndexPage(table, 9, 1, first.Next)
	require.ErrorIs(t, err, ErrInvalidCursor)
	require.Equal(t, RowPage{}, page)
	page, err = db.ScanRowsByIndexPage(table, 11, 1, first.Next, scanFixtureRow(1)[2])
	require.ErrorIs(t, err, ErrInvalidCursor)
	require.Equal(t, RowPage{}, page)
	prefix, err := schema.EncodeIndexPrefix(table, 11)
	require.NoError(t, err)
	raw, err := db.ScanPage(prefix, prefixEnd(prefix), 1, first.Next)
	require.ErrorIs(t, err, ErrInvalidCursor)
	require.Equal(t, EntryPage{}, raw)
	raw, err = db.ScanPage(prefix, prefixEnd(prefix), 1, nil)
	require.NoError(t, err)
	page, err = db.ScanRowsByIndexPage(table, 11, 1, raw.Next)
	require.ErrorIs(t, err, ErrInvalidCursor)
	require.Equal(t, RowPage{}, page)

	slices.Reverse(table.Columns)
	slices.Reverse(table.Indexes)
	clear(first.Rows[0])
	page, err = db.ScanRowsByIndexPage(table, 11, 2, first.Next)
	require.NoError(t, err)
	require.Equal(t, []schema.Row{scanFixtureRow(8), scanFixtureRow(12)}, page.Rows)
}

func TestDatabase_ScanRowsByIndexPageChecksOnlyReturnedReferences(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, _ := rowInsertFixture()
	table.Indexes = table.Indexes[:1]
	require.NoError(t, db.CreateTable(table))
	first, second := numberedRow(1), numberedRow(2)
	first[2], second[2] = recordpkg.NewBytes([]byte("same")), recordpkg.NewBytes([]byte("same"))
	require.NoError(t, db.InsertRow(table, first))
	index, err := schema.EncodeIndexEntry(table, 9, second)
	require.NoError(t, err)
	require.NoError(t, db.Put(index.Key, index.Value))
	page, err := db.ScanRowsByIndexPage(table, 9, 1, nil, first[2])
	require.NoError(t, err)
	require.Equal(t, []schema.Row{first}, page.Rows)
	require.NotNil(t, page.Next)
	last, err := db.ScanRowsByIndexPage(table, 9, 1, page.Next, first[2])
	require.ErrorContains(t, err, "missing primary row")
	require.Equal(t, RowPage{}, last)
}
