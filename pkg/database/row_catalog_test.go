package database

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func assertRowCatalogError(t *testing.T, db *Database, table schema.Table, row schema.Row, want error) {
	t.Helper()
	before := db.Stats()
	got, found, err := db.GetRow(table, row[table.PrimaryKeyColumnID])
	require.ErrorIs(t, err, want)
	require.False(t, found)
	require.Nil(t, got)
	got, found, err = db.GetRowByUniqueIndex(table, 7, row[2])
	require.ErrorIs(t, err, want)
	require.False(t, found)
	require.Nil(t, got)
	rows, err := db.ScanRowsByIndex(table, 9, 10)
	require.ErrorIs(t, err, want)
	require.Nil(t, rows)
	page, err := db.ScanRowsByIndexPage(table, 9, 10, nil)
	require.ErrorIs(t, err, want)
	require.Equal(t, RowPage{}, page)
	matched, err := db.UpdateRow(table, row)
	require.ErrorIs(t, err, want)
	require.False(t, matched)
	deleted, err := db.DeleteRow(table, row[table.PrimaryKeyColumnID])
	require.ErrorIs(t, err, want)
	require.False(t, deleted)
	require.ErrorIs(t, db.InsertRow(table, row), want)
	after := db.Stats()
	require.Equal(t, before.Writes, after.Writes)
	require.Equal(t, before.PageWrites, after.PageWrites)
	require.Equal(t, before.PageAppends, after.PageAppends)
	require.Equal(t, before.Syncs, after.Syncs)
}

func TestDatabase_RowOperationsRequireCatalog(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, row := rowInsertFixture()
	assertRowCatalogError(t, db, table, row, ErrTableNotFound)
	require.NoError(t, db.CreateTable(table))
	require.NoError(t, db.InsertRow(table, row))
	assertStoredRow(t, db, table, row)
}

func TestDatabase_RowOperationsRejectCatalogMismatch(t *testing.T) {
	for name, change := range map[string]func(*schema.Table, schema.Row){
		"table name":  func(table *schema.Table, _ schema.Row) { table.Name = "different" },
		"column name": func(table *schema.Table, _ schema.Row) { table.Columns[0].Name = "different" },
		"index name":  func(table *schema.Table, _ schema.Row) { table.Indexes[0].Name = "different" },
		"nullable":    func(table *schema.Table, _ schema.Row) { table.Columns[2].Nullable = true },
		"primary key": func(table *schema.Table, _ schema.Row) { table.PrimaryKeyColumnID = 3 },
		"omitted unique index": func(table *schema.Table, _ schema.Row) {
			table.Indexes = append(table.Indexes[:1], table.Indexes[2:]...)
		},
		"weakened unique index":    func(table *schema.Table, _ schema.Row) { table.Indexes[1].Unique = false },
		"different indexed column": func(table *schema.Table, _ schema.Row) { table.Indexes[1].ColumnIDs = []uint32{1} },
		"added index": func(table *schema.Table, _ schema.Row) {
			table.Indexes = append(table.Indexes, schema.Index{ID: 100, Name: "extra", ColumnIDs: []uint32{1}})
		},
		"different type": func(table *schema.Table, row schema.Row) {
			table.Columns[0].Type = recordpkg.TypeInt64
			row[1] = recordpkg.NewInt64(42)
		},
		"added column": func(table *schema.Table, row schema.Row) {
			table.Columns = append(table.Columns, schema.Column{ID: 100, Name: "extra", Type: recordpkg.TypeBool})
			row[100] = recordpkg.NewBool(true)
		},
	} {
		t.Run(name, func(t *testing.T) {
			db, _ := openWriterDatabase(t)
			table, row := rowInsertFixture()
			require.NoError(t, db.CreateTable(table))
			changed := cloneRowPlanTable(table)
			change(&changed, row)
			assertRowCatalogError(t, db, changed, row, ErrSchemaMismatch)
			_, originalRow := rowInsertFixture()
			require.NoError(t, db.InsertRow(table, originalRow))
			assertStoredRow(t, db, table, originalRow)
		})
	}
}

func TestDatabase_InsertCannotBypassUniqueIndex(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, first := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	require.NoError(t, db.InsertRow(table, first))
	second := numberedRow(999)
	second[2] = first[2]
	changed := cloneRowPlanTable(table)
	changed.Indexes = nil
	before := db.Stats()
	require.ErrorIs(t, db.InsertRow(changed, second), ErrSchemaMismatch)
	require.ErrorIs(t, db.InsertRow(table, second), ErrUniqueConflict)
	require.Equal(t, before.PageWrites, db.Stats().PageWrites)
	require.Equal(t, before.Syncs, db.Stats().Syncs)
	_, found, err := db.GetRow(table, second[1])
	require.NoError(t, err)
	require.False(t, found)
}

func TestDatabase_RowCatalogDefinitionOrder(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	slices.Reverse(table.Columns)
	slices.Reverse(table.Indexes)
	require.NoError(t, db.InsertRow(table, row))
	got, found, err := db.GetRow(table, row[1])
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, row, got)
	got, found, err = db.GetRowByUniqueIndex(table, 7, row[2])
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, row, got)
	rows, err := db.ScanRowsByIndex(table, 9, 10)
	require.NoError(t, err)
	require.Equal(t, []schema.Row{row}, rows)
	page, err := db.ScanRowsByIndexPage(table, 9, 10, nil)
	require.NoError(t, err)
	require.Equal(t, []schema.Row{row}, page.Rows)
}

func TestDatabase_RowCatalogChecksFollowWriterOrder(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, row := rowInsertFixture()
	entered, release := blockFirstFlush(t, db)
	first := make(chan error, 1)
	go func() { first <- db.Put([]byte("initial"), nil) }()
	<-entered
	beforeCreate, err := makeRowInsertRequest(table, row)
	require.NoError(t, err)
	create, err := makeCreateTableRequest(table)
	require.NoError(t, err)
	afterCreate, err := makeRowInsertRequest(table, row)
	require.NoError(t, err)
	changed := cloneRowPlanTable(table)
	changed.Indexes = nil
	mismatch, err := makeRowInsertRequest(changed, numberedRow(999))
	require.NoError(t, err)

	for _, request := range []*writeRequest{beforeCreate, create, afterCreate, mismatch} {
		db.writes <- request
	}

	release()
	require.NoError(t, waitWriteResult(t, first))
	require.ErrorIs(t, waitWriteResult(t, beforeCreate.result), ErrTableNotFound)
	require.NoError(t, waitWriteResult(t, create.result))
	require.NoError(t, waitWriteResult(t, afterCreate.result))
	require.ErrorIs(t, waitWriteResult(t, mismatch.result), ErrSchemaMismatch)
	require.Equal(t, uint64(7), db.Stats().Writes)
	require.Equal(t, uint64(2), db.Stats().Syncs)
	assertStoredRow(t, db, table, row)
}

func TestDatabase_InsertRechecksCatalogAfterQueueing(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	stale, err := makeRowInsertRequest(table, row)
	require.NoError(t, err)
	changed := cloneRowPlanTable(table)
	changed.Indexes = nil
	definition, err := makeCreateTableRequest(changed)
	require.NoError(t, err)
	entered, release := blockFirstFlush(t, db)
	first := make(chan error, 1)
	go func() { first <- db.Put([]byte("initial"), nil) }()
	<-entered
	replace, err := makeWriteRequest(definition.entries[:1])
	require.NoError(t, err)
	db.writes <- replace
	db.writes <- stale
	release()
	require.NoError(t, waitWriteResult(t, first))
	require.NoError(t, waitWriteResult(t, replace.result))
	require.ErrorIs(t, waitWriteResult(t, stale.result), ErrSchemaMismatch)
	require.Equal(t, uint64(4), db.Stats().Writes)
	_, found, err := db.GetRow(changed, row[1])
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, db.InsertRow(changed, row))
}

func TestDatabase_RowOperationsRejectCorruptCatalog(t *testing.T) {
	for _, nameReference := range []bool{false, true} {
		db, _ := openWriterDatabase(t)
		table, row := rowInsertFixture()
		require.NoError(t, db.CreateTable(table))

		key := catalogIDKey(table.ID)
		if nameReference {
			key = catalogNameKey(table.Name)
		}

		require.NoError(t, db.Put(key, []byte{0xff}))
		_, _, err := db.GetRow(table, row[1])
		require.ErrorIs(t, err, ErrInvalidCatalog)
		assertRowCatalogError(t, db, table, row, ErrInvalidCatalog)
		require.ErrorIs(t, db.Put([]byte("stopped"), nil), ErrWriteFailed)
	}
}
