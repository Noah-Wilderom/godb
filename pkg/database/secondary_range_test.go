package database

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func TestSecondaryRangeBoundaries(t *testing.T) {
	text := func(s string) recordpkg.Value { v, err := recordpkg.NewText(s); require.NoError(t, err); return v }

	uuid := func(s string) recordpkg.Value { v, err := recordpkg.NewUUID(s); require.NoError(t, err); return v }
	for _, test := range []struct {
		name string
		typ  recordpkg.Type
		keys []recordpkg.Value
	}{
		{"signed", recordpkg.TypeInt64, []recordpkg.Value{recordpkg.NewInt64(math.MinInt64), recordpkg.NewInt64(-1), recordpkg.NewInt64(0), recordpkg.NewInt64(math.MaxInt64)}},
		{"unsigned", recordpkg.TypeUint64, []recordpkg.Value{recordpkg.NewUint64(0), recordpkg.NewUint64(1), recordpkg.NewUint64(math.MaxUint64)}},
		{"text", recordpkg.TypeText, []recordpkg.Value{text(""), text("a"), text("a\x00"), text("aa"), text("b")}},
		{"uuid", recordpkg.TypeUUID, []recordpkg.Value{uuid("00000000-0000-0000-0000-000000000000"), uuid("00000000-0000-0000-0000-000000000001"), uuid("ffffffff-ffff-ffff-ffff-ffffffffffff")}},
		{"bytes", recordpkg.TypeBytes, []recordpkg.Value{recordpkg.NewBytes([]byte{}), recordpkg.NewBytes([]byte{0}), recordpkg.NewBytes([]byte{0, 255}), recordpkg.NewBytes([]byte{255})}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, _ := openIdentityDatabase(t)
			table := schema.Table{ID: 1, Name: "ranges", PrimaryKeyColumnID: 1, Columns: []schema.Column{
				{ID: 1, Name: "id", Type: recordpkg.TypeUint64}, {ID: 2, Name: "tenant", Type: recordpkg.TypeInt64, Nullable: true}, {ID: 3, Name: "value", Type: test.typ, Nullable: true},
			}, Indexes: []schema.Index{{ID: 1, Name: "ordinary", ColumnIDs: []uint32{2, 3}}, {ID: 2, Name: "unique_tail", ColumnIDs: []uint32{2, 3, 1}, Unique: true}}}
			require.NoError(t, db.CreateTable(table))
			groups := []recordpkg.Value{recordpkg.NewNull(), recordpkg.NewInt64(1), recordpkg.NewInt64(2)}
			rows := make([][]schema.Row, len(groups))
			id := uint64(0)

			for g, group := range groups {
				for _, value := range append([]recordpkg.Value{recordpkg.NewNull()}, test.keys...) {
					for range 3 {
						id++
						row := schema.Row{1: recordpkg.NewUint64(id), 2: group, 3: value}
						require.NoError(t, db.InsertRow(table, row))

						if !value.IsNull() {
							rows[g] = append(rows[g], row)
						}
					}
				}
			}

			for _, index := range table.Indexes {
				for g, group := range groups[:2] {
					var (
						cursor *ScanCursor
						paged  []schema.Row
					)

					for {
						page, err := db.ScanRowsByIndexRangePage(table, index.ID, nil, nil, 2, cursor, group)
						require.NoError(t, err)

						paged = append(paged, page.Rows...)
						if page.Next == nil {
							break
						}

						require.Less(t, len(paged), len(rows[g]))
						cursor = page.Next
					}

					require.Equal(t, rows[g], paged)

					for lo := -1; lo < len(test.keys); lo++ {
						for hi := -1; hi < len(test.keys); hi++ {
							for _, incLo := range []bool{false, true} {
								for _, incHi := range []bool{false, true} {
									var lower, upper *KeyBound

									if lo >= 0 {
										lower = &KeyBound{Value: test.keys[lo], Inclusive: incLo}
									}

									if hi >= 0 {
										upper = &KeyBound{Value: test.keys[hi], Inclusive: incHi}
									}

									page, err := db.ScanRowsByIndexRangePage(table, index.ID, lower, upper, 100, nil, group)
									require.NoError(t, err)

									var want []schema.Row

									for i, row := range rows[g] {
										k := i / 3
										if (lo < 0 || k > lo || (k == lo && incLo)) && (hi < 0 || k < hi || (k == hi && incHi)) {
											want = append(want, row)
										}
									}

									require.Equal(t, want, page.Rows, "index=%d group=%d lower=%d/%v upper=%d/%v", index.ID, g, lo, incLo, hi, incHi)
									require.Nil(t, page.Next)
								}
							}
						}
					}
				}
			}
		})
	}
}

func TestSecondaryRangeCursorValidationAndReopen(t *testing.T) {
	db, path, table := indexScanFixture(t)
	lower := &KeyBound{Value: recordpkg.NewInt64(5), Inclusive: true}
	upper := &KeyBound{Value: recordpkg.NewInt64(21), Inclusive: true}
	prefix := recordpkg.NewBytes([]byte("a"))
	page, err := db.ScanRowsByIndexRangePage(table, 11, lower, upper, 2, nil, prefix)
	require.NoError(t, err)
	require.Equal(t, []schema.Row{scanFixtureRow(5), scanFixtureRow(9)}, page.Rows)
	cursor := page.Next
	require.NotNil(t, cursor)
	next, err := db.ScanRowsByIndexRangePage(table, 11, lower, upper, 2, cursor, prefix)
	require.NoError(t, err)
	require.Equal(t, []schema.Row{scanFixtureRow(13), scanFixtureRow(17)}, next.Rows)
	next, err = db.ScanRowsByIndexRangePage(table, 11, lower, upper, 2, next.Next, prefix)
	require.NoError(t, err)
	require.Equal(t, []schema.Row{scanFixtureRow(21)}, next.Rows)
	require.Nil(t, next.Next)
	changed := *lower
	changed.Inclusive = false
	_, err = db.ScanRowsByIndexRangePage(table, 11, &changed, upper, 2, cursor, prefix)
	require.ErrorIs(t, err, ErrInvalidCursor)
	_, err = db.ScanRowsByIndexRangePage(table, 11, lower, upper, 2, cursor, recordpkg.NewNull())
	require.ErrorIs(t, err, ErrInvalidCursor)
	_, err = db.ScanRowsByIndexPage(table, 11, 2, cursor, prefix)
	require.ErrorIs(t, err, ErrInvalidCursor)

	for _, bound := range []KeyBound{{Value: recordpkg.NewNull()}, {Value: recordpkg.NewBytes([]byte("wrong"))}} {
		_, err = db.ScanRowsByIndexRangePage(table, 11, &bound, nil, 2, nil, prefix)
		require.Error(t, err)
		_, err = db.ScanRowsByIndexRangePage(table, 11, nil, &bound, 2, nil, prefix)
		require.Error(t, err)
	}

	_, err = db.ScanRowsByIndexRangePage(table, 11, nil, nil, 2, nil, prefix, recordpkg.NewInt64(1))
	require.ErrorIs(t, err, ErrInvalidScan)

	for _, limit := range []int{0, -1, MaxScanEntries + 1} {
		_, err = db.ScanRowsByIndexRangePage(table, 11, lower, upper, limit, nil, prefix)
		require.ErrorIs(t, err, ErrInvalidScan)
	}

	mismatch := cloneTableDefinition(table)
	mismatch.Name = "changed"
	_, err = db.ScanRowsByIndexRangePage(mismatch, 11, upper, lower, 2, nil, prefix)
	require.ErrorIs(t, err, ErrSchemaMismatch)
	require.NoError(t, db.Close())
	_, err = db.ScanRowsByIndexRangePage(table, 11, upper, lower, 2, nil, prefix)
	require.ErrorIs(t, err, ErrClosed)
	require.NoError(t, Validate(path))
	reopened, err := Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	_, err = reopened.ScanRowsByIndexRangePage(table, 11, lower, upper, 2, cursor, prefix)
	require.ErrorIs(t, err, ErrInvalidCursor)
	page, err = reopened.ScanRowsByIndexRangePage(table, 11, lower, upper, 10, nil, prefix)
	require.NoError(t, err)
	require.Len(t, page.Rows, 5)
}
