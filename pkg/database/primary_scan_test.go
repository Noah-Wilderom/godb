package database

import (
	"testing"

	"github.com/stretchr/testify/require"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func TestScanRowsPageIsolationPaginationAndOwnership(t *testing.T) {
	db, path := openIdentityDatabase(t)
	table, _ := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	other := cloneTableDefinition(table)
	other.ID, other.Name = 2, "other"
	require.NoError(t, db.CreateTable(other))
	require.NoError(t, db.InsertRow(other, numberedRow(9)))
	require.NoError(t, db.Put([]byte("raw"), []byte("value")))
	page, err := db.ScanRowsPage(table, 2, nil)
	require.NoError(t, err)
	require.Empty(t, page.Rows)
	require.Nil(t, page.Next)

	for _, id := range []uint64{3, 1, 2} {
		require.NoError(t, db.InsertRow(table, numberedRow(id)))
	}

	before := db.Stats().Writes
	page, err = db.ScanRowsPage(table, 2, nil)
	require.NoError(t, err)
	require.Equal(t, []schema.Row{numberedRow(1), numberedRow(2)}, page.Rows)
	require.NotNil(t, page.Next)
	firstCursor := page.Next
	page.Rows[0][2] = recordpkg.NewBytes([]byte("mutated"))
	assertStoredRow(t, db, table, numberedRow(1))
	require.Equal(t, before, db.Stats().Writes)
	_, err = db.DeleteRow(table, recordpkg.NewUint64(2))
	require.NoError(t, err)
	require.NoError(t, db.InsertRow(table, numberedRow(4)))
	page, err = db.ScanRowsPage(table, 2, firstCursor)
	require.NoError(t, err)
	require.Equal(t, []schema.Row{numberedRow(3), numberedRow(4)}, page.Rows)
	require.Nil(t, page.Next)
	_, err = db.ScanRowsPage(other, 2, firstCursor)
	require.ErrorIs(t, err, ErrInvalidCursor)
	prefix, err := schema.EncodePrimaryPrefix(table)
	require.NoError(t, err)
	raw, err := db.ScanPage(prefix, prefixEnd(prefix), 1, nil)
	require.NoError(t, err)
	_, err = db.ScanRowsPage(table, 2, raw.Next)
	require.ErrorIs(t, err, ErrInvalidCursor)
	_, err = db.ScanPage(prefix, prefixEnd(prefix), 2, firstCursor)
	require.ErrorIs(t, err, ErrInvalidCursor)
	changed := cloneTableDefinition(table)
	changed.Columns[1].Nullable = !changed.Columns[1].Nullable
	_, err = db.ScanRowsPage(changed, 2, firstCursor)
	require.ErrorIs(t, err, ErrInvalidCursor)

	for _, limit := range []int{0, -1, MaxScanEntries + 1} {
		_, err = db.ScanRowsPage(table, limit, nil)
		require.Error(t, err)
	}

	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
	_, err = db.ScanRowsPage(table, 2, nil)
	require.ErrorIs(t, err, ErrClosed)
	reopened, err := Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	_, err = reopened.ScanRowsPage(table, 2, firstCursor)
	require.ErrorIs(t, err, ErrInvalidCursor)
	page, err = reopened.ScanRowsPage(table, 3, nil)
	require.NoError(t, err)
	require.Equal(t, []schema.Row{numberedRow(1), numberedRow(3), numberedRow(4)}, page.Rows)
	require.Nil(t, page.Next)
}

func TestScanRowsPageRejectsCorruptPrimaryRows(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "malformed", true: "wrong-key"}[mismatch], func(t *testing.T) {
			db, _ := openIdentityDatabase(t)
			table, _ := rowInsertFixture()
			require.NoError(t, db.CreateTable(table))
			require.NoError(t, db.InsertRow(table, numberedRow(1)))
			key, err := schema.EncodePrimaryKey(table, recordpkg.NewUint64(2))
			require.NoError(t, err)

			payload := []byte{255}
			if mismatch {
				payload, err = schema.EncodeRow(table, numberedRow(3))
				require.NoError(t, err)
			}

			require.NoError(t, db.Put(key, payload))
			page, err := db.ScanRowsPage(table, 2, nil)
			require.ErrorIs(t, err, ErrInvalidData)
			require.Equal(t, RowPage{}, page)
		})
	}
}

func TestScanRowsPageAtomicSnapshot(t *testing.T) {
	db, _ := openIdentityDatabase(t)
	table, _ := rowInsertFixture()
	table.Indexes = nil
	require.NoError(t, db.CreateTable(table))
	first, second := numberedRow(1), numberedRow(2)
	first[3], second[3] = recordpkg.NewInt64(0), recordpkg.NewInt64(0)
	require.NoError(t, db.InsertRow(table, first))
	require.NoError(t, db.InsertRow(table, second))
	done := make(chan error, 1)
	go func() {
		for i := range int64(50) {
			first[3], second[3] = recordpkg.NewInt64(i), recordpkg.NewInt64(i)

			command, err := EncodeCommand(Command{Operations: []CommandOperation{{Kind: CommandUpdateRow, Table: &table, Row: first}, {Kind: CommandUpdateRow, Table: &table, Row: second}}})
			if err != nil {
				done <- err
				return
			}

			_, err = db.ApplyCommand(command)
			if err != nil {
				done <- err
				return
			}
		}

		done <- nil
	}()

	for range 50 {
		page, err := db.ScanRowsPage(table, 2, nil)
		require.NoError(t, err)
		require.Len(t, page.Rows, 2)
		require.Equal(t, page.Rows[0][3], page.Rows[1][3])
	}

	require.NoError(t, <-done)
}
