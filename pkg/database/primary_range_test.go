package database

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func TestPrimaryRangeBoundaries(t *testing.T) {
	text := func(s string) recordpkg.Value { v, err := recordpkg.NewText(s); require.NoError(t, err); return v }

	uuid := func(s string) recordpkg.Value { v, err := recordpkg.NewUUID(s); require.NoError(t, err); return v }
	for _, test := range []struct {
		name string
		typ  recordpkg.Type
		keys []recordpkg.Value
	}{
		{"signed", recordpkg.TypeInt64, []recordpkg.Value{recordpkg.NewInt64(math.MinInt64), recordpkg.NewInt64(-1), recordpkg.NewInt64(0), recordpkg.NewInt64(1), recordpkg.NewInt64(math.MaxInt64)}},
		{"unsigned", recordpkg.TypeUint64, []recordpkg.Value{recordpkg.NewUint64(0), recordpkg.NewUint64(1), recordpkg.NewUint64(1 << 63), recordpkg.NewUint64(math.MaxUint64)}},
		{"text", recordpkg.TypeText, []recordpkg.Value{text(""), text("a"), text("a\x00"), text("a\x00b"), text("aa"), text("b"), text("é")}},
		{"bytes", recordpkg.TypeBytes, []recordpkg.Value{recordpkg.NewBytes([]byte{}), recordpkg.NewBytes([]byte{0}), recordpkg.NewBytes([]byte{0, 0}), recordpkg.NewBytes([]byte{0, 1}), recordpkg.NewBytes([]byte{1}), recordpkg.NewBytes([]byte{255})}},
		{"uuid", recordpkg.TypeUUID, []recordpkg.Value{uuid("00000000-0000-0000-0000-000000000000"), uuid("00000000-0000-0000-0000-000000000001"), uuid("ffffffff-ffff-ffff-ffff-ffffffffffff")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, _ := openIdentityDatabase(t)
			table := schema.Table{ID: 1, Name: "bounds", PrimaryKeyColumnID: 1, Columns: []schema.Column{{ID: 1, Name: "id", Type: test.typ}}, Indexes: []schema.Index{{ID: 1, Name: "copy", ColumnIDs: []uint32{1}}}}
			require.NoError(t, db.CreateTable(table))
			other := cloneTableDefinition(table)
			other.ID, other.Name = 2, "other"
			require.NoError(t, db.CreateTable(other))
			require.NoError(t, db.InsertRow(other, schema.Row{1: test.keys[0]}))

			for _, key := range test.keys {
				require.NoError(t, db.InsertRow(table, schema.Row{1: key}))
			}

			for lo := -1; lo < len(test.keys); lo++ {
				for hi := -1; hi < len(test.keys); hi++ {
					for _, inclusiveLo := range []bool{false, true} {
						for _, inclusiveHi := range []bool{false, true} {
							var lower, upper *KeyBound

							if lo >= 0 {
								lower = &KeyBound{Value: test.keys[lo], Inclusive: inclusiveLo}
							}

							if hi >= 0 {
								upper = &KeyBound{Value: test.keys[hi], Inclusive: inclusiveHi}
							}

							page, err := db.ScanRowsRangePage(table, lower, upper, MaxScanEntries, nil)
							require.NoError(t, err)
							want := make([]schema.Row, 0)

							for i, key := range test.keys {
								if (lo < 0 || i > lo || (inclusiveLo && i == lo)) && (hi < 0 || i < hi || (inclusiveHi && i == hi)) {
									want = append(want, schema.Row{1: key})
								}
							}

							require.Equal(t, want, page.Rows, "lower=%d inclusive=%v upper=%d inclusive=%v", lo, inclusiveLo, hi, inclusiveHi)
							require.Nil(t, page.Next)
						}
					}
				}
			}
		})
	}
}

func TestPrimaryRangeCursorValidationAndReopen(t *testing.T) {
	db, path := openIdentityDatabase(t)
	table, _ := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))

	for id := uint64(1); id <= 6; id++ {
		require.NoError(t, db.InsertRow(table, numberedRow(id)))
	}

	lower := &KeyBound{Value: recordpkg.NewUint64(1), Inclusive: false}
	upper := &KeyBound{Value: recordpkg.NewUint64(5), Inclusive: true}
	page, err := db.ScanRowsRangePage(table, lower, upper, 2, nil)
	require.NoError(t, err)
	require.Equal(t, []schema.Row{numberedRow(2), numberedRow(3)}, page.Rows)
	require.NotNil(t, page.Next)
	cursor := page.Next
	_, err = db.DeleteRow(table, recordpkg.NewUint64(3))
	require.NoError(t, err)
	page, err = db.ScanRowsRangePage(table, lower, upper, 2, cursor)
	require.NoError(t, err)
	require.Equal(t, []schema.Row{numberedRow(4), numberedRow(5)}, page.Rows)
	require.Nil(t, page.Next)
	changed := *lower
	changed.Inclusive = true
	_, err = db.ScanRowsRangePage(table, &changed, upper, 2, cursor)
	require.ErrorIs(t, err, ErrInvalidCursor)
	changed = *upper
	changed.Inclusive = false
	_, err = db.ScanRowsRangePage(table, lower, &changed, 2, cursor)
	require.ErrorIs(t, err, ErrInvalidCursor)
	_, err = db.ScanRowsPage(table, 2, cursor)
	require.ErrorIs(t, err, ErrInvalidCursor)

	for _, bound := range []KeyBound{{Value: recordpkg.NewNull()}, {Value: recordpkg.NewInt64(1)}} {
		_, err = db.ScanRowsRangePage(table, &bound, nil, 2, nil)
		require.Error(t, err)
		_, err = db.ScanRowsRangePage(table, nil, &bound, 2, nil)
		require.Error(t, err)
	}

	for _, limit := range []int{0, -1, MaxScanEntries + 1} {
		_, err = db.ScanRowsRangePage(table, lower, upper, limit, nil)
		require.Error(t, err)
	}

	mismatch := cloneTableDefinition(table)
	mismatch.Name = "changed"
	_, err = db.ScanRowsRangePage(mismatch, upper, lower, 2, nil)
	require.ErrorIs(t, err, ErrSchemaMismatch)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
	_, err = db.ScanRowsRangePage(table, upper, lower, 2, nil)
	require.ErrorIs(t, err, ErrClosed)
	reopened, err := Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	_, err = reopened.ScanRowsRangePage(table, lower, upper, 2, cursor)
	require.ErrorIs(t, err, ErrInvalidCursor)
	page, err = reopened.ScanRowsRangePage(table, lower, upper, 3, nil)
	require.NoError(t, err)
	require.Equal(t, []schema.Row{numberedRow(2), numberedRow(4), numberedRow(5)}, page.Rows)
	require.Nil(t, page.Next)
}

func TestPrimaryRangeSeeksAndRejectsCorruptRows(t *testing.T) {
	db, _ := openIdentityDatabase(t)
	table, _ := rowInsertFixture()
	table.Indexes = nil
	require.NoError(t, db.CreateTable(table))

	for id := uint64(1); id <= 1100; id++ {
		require.NoError(t, db.InsertRow(table, numberedRow(id)))
	}

	before := db.Stats().PageReads
	page, err := db.ScanRowsPage(table, MaxScanEntries, nil)
	require.NoError(t, err)
	require.NotNil(t, page.Next)
	fullReads := db.Stats().PageReads - before
	before = db.Stats().PageReads
	lower := &KeyBound{Value: recordpkg.NewUint64(1098)}
	page, err = db.ScanRowsRangePage(table, lower, nil, 10, nil)
	require.NoError(t, err)
	require.Equal(t, []schema.Row{numberedRow(1099), numberedRow(1100)}, page.Rows)
	require.Nil(t, page.Next)
	require.Less(t, db.Stats().PageReads-before, fullReads)
	key, err := schema.EncodePrimaryKey(table, recordpkg.NewUint64(1100))
	require.NoError(t, err)
	require.NoError(t, db.Put(key, []byte{255}))
	page, err = db.ScanRowsRangePage(table, lower, nil, 10, nil)
	require.ErrorIs(t, err, ErrInvalidData)
	require.Equal(t, RowPage{}, page)
}
