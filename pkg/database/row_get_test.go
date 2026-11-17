package database

import (
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func TestDatabase_GetRowReopenAndTableIsolation(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	row[2] = recordpkg.NewNull()
	require.NoError(t, db.InsertRow(table, row))
	otherTable := cloneRowPlanTable(table)
	otherTable.ID = 2
	_, otherRow := rowInsertFixture()
	otherTable.Name = "other_users"
	require.NoError(t, db.CreateTable(otherTable))
	require.NoError(t, db.InsertRow(otherTable, otherRow))
	require.NoError(t, db.Close())
	reopened, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	got, found, err := reopened.GetRow(table, row[1])
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, row, got)
	require.True(t, got[2].IsNull())
	got, found, err = reopened.GetRow(otherTable, row[1])
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, otherRow, got)
	got, found, err = reopened.GetRow(table, recordpkg.NewUint64(999))
	require.NoError(t, err)
	require.False(t, found)
	require.Nil(t, got)

	slices.Reverse(table.Columns)
	got, found, err = reopened.GetRow(table, row[1])
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, row, got)
}

func TestDatabase_GetRowPrimaryKeyTypes(t *testing.T) {
	text, err := recordpkg.NewText("pk\x00🌳")
	require.NoError(t, err)
	empty, err := recordpkg.NewText("")
	require.NoError(t, err)
	id, err := recordpkg.NewUUID("00112233-4455-6677-8899-aabbccddeeff")
	require.NoError(t, err)

	for _, key := range []recordpkg.Value{
		recordpkg.NewInt64(math.MinInt64), recordpkg.NewInt64(math.MaxInt64),
		recordpkg.NewUint64(0), recordpkg.NewUint64(math.MaxUint64), text, empty,
		recordpkg.NewBytes([]byte{0, 0xff}), recordpkg.NewBytes(nil), id,
	} {
		db, _ := openWriterDatabase(t)
		table, row := rowInsertFixture()
		table.Columns[0].Type = key.Type()
		require.NoError(t, db.CreateTable(table))
		row[1] = key
		require.NoError(t, db.InsertRow(table, row))
		got, found, err := db.GetRow(table, key)
		require.NoError(t, err)
		require.True(t, found)
		wantBytes, err := schema.EncodeRow(table, row)
		require.NoError(t, err)
		gotBytes, err := schema.EncodeRow(table, got)
		require.NoError(t, err)
		require.Equal(t, wantBytes, gotBytes)
	}
}

func TestDatabase_GetRowInvalidInputDoesNotReadStorage(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, _ := rowInsertFixture()

	for _, key := range []recordpkg.Value{recordpkg.NewNull(), recordpkg.NewInt64(42), recordpkg.NewBool(true)} {
		row, found, err := db.GetRow(table, key)
		require.ErrorContains(t, err, "encode primary key")
		require.False(t, found)
		require.Nil(t, row)
	}

	row, found, err := db.GetRow(schema.Table{}, recordpkg.NewUint64(1))
	require.Error(t, err)
	require.False(t, found)
	require.Nil(t, row)
	require.Zero(t, db.Stats().Reads)
	require.Zero(t, db.Stats().PageReads)
}

func TestDatabase_GetRowRejectsMalformedStorage(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	key, err := schema.EncodePrimaryKey(table, row[1])
	require.NoError(t, err)
	valid, err := schema.EncodeRow(table, row)
	require.NoError(t, err)
	otherTable := cloneRowPlanTable(table)
	otherTable.ID = 2
	wrongTable, err := schema.EncodeRow(otherTable, row)
	require.NoError(t, err)

	for _, payload := range [][]byte{nil, {0xff}, valid[:len(valid)-1], append(slices.Clone(valid), 0), wrongTable} {
		require.NoError(t, db.Put(key, payload))
		got, found, err := db.GetRow(table, row[1])
		require.ErrorContains(t, err, "decode row")
		require.False(t, found)
		require.Nil(t, got)
	}

	row[1] = recordpkg.NewUint64(999)
	wrongKey, err := schema.EncodeRow(table, row)
	require.NoError(t, err)
	require.NoError(t, db.Put(key, wrongKey))
	got, found, err := db.GetRow(table, recordpkg.NewUint64(42))
	require.ErrorContains(t, err, "primary key does not match lookup key")
	require.False(t, found)
	require.Nil(t, got)
	require.NoError(t, db.Put(key, valid))
	_, found, err = db.GetRow(table, recordpkg.NewUint64(42))
	require.NoError(t, err)
	require.True(t, found)
}

func TestDatabase_GetRowRejectsSchemaMismatch(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	require.NoError(t, db.InsertRow(table, row))
	table.Columns[1].Type = recordpkg.TypeText
	got, found, err := db.GetRow(table, row[1])
	require.ErrorIs(t, err, ErrSchemaMismatch)
	require.False(t, found)
	require.Nil(t, got)
}

func TestDatabase_GetRowOwnsReturnedData(t *testing.T) {
	db, _ := openWriterDatabase(t)
	table, row := rowInsertFixture()
	table.Columns = append(table.Columns, schema.Column{ID: 100, Name: "metadata", Type: recordpkg.TypeMap})
	require.NoError(t, db.CreateTable(table))
	row[100] = recordpkg.NewMap(map[string]recordpkg.Value{"nested": recordpkg.NewBytes([]byte{1, 2, 3})})
	require.NoError(t, db.InsertRow(table, row))
	got, found, err := db.GetRow(table, row[1])
	require.NoError(t, err)
	require.True(t, found)
	nested, err := got[100].Map()
	require.NoError(t, err)
	data, err := nested["nested"].Bytes()
	require.NoError(t, err)
	clear(data)
	clear(nested)
	clear(got)
	again, found, err := db.GetRow(table, row[1])
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, row, again)
}

func TestDatabase_GetRowStorageAndClosedErrors(t *testing.T) {
	for _, stage := range []string{"Read", "Failed", "Closed"} {
		t.Run(stage, func(t *testing.T) {
			db, _ := openWriterDatabase(t)
			table, row := rowInsertFixture()
			require.NoError(t, db.CreateTable(table))
			cause := errors.New("injected read failure")
			want := cause

			switch stage {
			case "Read":
				db.pager = &hookedStore{pageStore: db.pager, read: func(uint64) ([]byte, error) { return nil, cause }}
			case "Failed":
				db.pager = &hookedStore{pageStore: db.pager, sync: func() error { return cause }}
				require.ErrorIs(t, db.InsertRow(table, row), ErrWriteFailed)
				want = ErrWriteFailed
			case "Closed":
				require.NoError(t, db.Close())
				want = ErrClosed
			}

			got, found, err := db.GetRow(table, row[1])
			require.ErrorIs(t, err, want)
			require.False(t, found)
			require.Nil(t, got)
		})
	}
}
