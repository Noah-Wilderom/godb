package database

import (
	"bytes"
	"encoding/binary"
	"os"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/pager"
	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func validationEntries(t testing.TB, table schema.Table, rows ...schema.Row) []Entry {
	t.Helper()
	request, err := makeCreateTableRequest(table)
	require.NoError(t, err)
	entries := request.entries

	for _, row := range rows {
		plan, err := PlanRowInsert(table, row)
		require.NoError(t, err)
		entries = append(entries, plan.Primary)

		for _, index := range plan.Indexes {
			entries = append(entries, Entry{Key: index.Key, Value: index.Value})
		}
	}

	return entries
}

func writeValidationEntries(t testing.TB, entries []Entry) string {
	t.Helper()
	slices.SortFunc(entries, func(a, b Entry) int { return bytes.Compare(a.Key, b.Key) })
	fixture := make([]testEntry, len(entries))
	size := 3

	for i, entry := range entries {
		size += 4 + len(entry.Key) + len(entry.Value)
		fixture[i] = testEntry{key: entry.Key, value: entry.Value}
	}

	require.LessOrEqual(t, size, 4096)
	return writeDatabaseFixture(t, databaseBytesWithEntries(fixture))
}

func TestValidate_CatalogRowsAndIndexesAcrossSplits(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, _ := rowInsertFixture()
	table.Indexes = append(table.Indexes, schema.Index{ID: 100, Name: "composite", ColumnIDs: []uint32{2, 3}, Unique: true})
	require.NoError(t, db.CreateTable(table))

	for id := uint64(1); id <= 300; id++ {
		row := numberedRow(id)
		if id%2 == 0 {
			row[2] = recordpkg.NewNull()
		}

		require.NoError(t, db.InsertRow(table, row))
	}

	other := cloneRowPlanTable(table)
	other.ID, other.Name, other.Indexes = 2, "no_indexes", nil
	require.NoError(t, db.CreateTable(other))
	require.NoError(t, db.InsertRow(other, numberedRow(1)))
	other.ID, other.Name = 3, "empty_table"
	require.NoError(t, db.CreateTable(other))
	require.NoError(t, db.Put([]byte("raw"), []byte("untyped")))
	require.NoError(t, db.Put(nil, nil))
	require.Positive(t, db.Stats().SplitOperations)
	require.NoError(t, db.Close())
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, Validate(path))
	assertFileContents(t, path, before)
}

func TestValidate_RejectsCatalogInconsistency(t *testing.T) {
	for name, change := range map[string]func([]Entry) []Entry{
		"missing definition": func(entries []Entry) []Entry { return entries[1:] },
		"missing name":       func(entries []Entry) []Entry { return entries[:1] },
		"bad definition":     func(entries []Entry) []Entry { entries[0].Value = []byte{0xff}; return entries },
		"wrong ID":           func(entries []Entry) []Entry { entries[0].Key = catalogIDKey(2); return entries },
		"bad reference":      func(entries []Entry) []Entry { entries[1].Value = []byte{0}; return entries },
		"dangling reference": func(entries []Entry) []Entry { entries[1].Value = catalogIDKey(99); return entries },
		"zero reference":     func(entries []Entry) []Entry { entries[1].Value = catalogIDKey(0); return entries },
		"alias": func(entries []Entry) []Entry {
			return append(entries, Entry{Key: catalogNameKey("alias"), Value: catalogIDKey(1)})
		},
		"invalid name":  func(entries []Entry) []Entry { entries[1].Key = []byte{0, 2, 0xff}; return entries },
		"empty name":    func(entries []Entry) []Entry { entries[1].Key = []byte{0, 2}; return entries },
		"unknown kind":  func(entries []Entry) []Entry { return append(entries, Entry{Key: []byte{0, 255}}) },
		"truncated key": func(entries []Entry) []Entry { return append(entries, Entry{Key: []byte{0}}) },
		"long ID key":   func(entries []Entry) []Entry { entries[0].Key = append(entries[0].Key, 0); return entries },
	} {
		t.Run(name, func(t *testing.T) {
			table, _ := rowInsertFixture()
			path := writeValidationEntries(t, change(validationEntries(t, table)))
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			require.ErrorIs(t, Validate(path), ErrInvalidCatalog)
			assertFileContents(t, path, before)
		})
	}
}

func TestValidate_RejectsRowAndIndexInconsistency(t *testing.T) {
	for _, scenario := range []string{
		"missing index", "wrong reference", "malformed row", "wrong primary", "trailing primary key",
		"missing row", "stale index", "extra index suffix", "unknown index", "uncataloged table",
		"short key", "zero table", "unique violation", "wrong table reference",
	} {
		t.Run(scenario, func(t *testing.T) {
			table, row := rowInsertFixture()
			entries := validationEntries(t, table, row)
			plan, err := PlanRowInsert(table, row)
			require.NoError(t, err)

			switch scenario {
			case "missing index":
				entries = entries[:len(entries)-1]
			case "wrong reference":
				entries[3].Value = []byte("not_a_primary_key")
			case "malformed row":
				entries[2].Value = []byte{0xff}
			case "wrong primary":
				row[1] = recordpkg.NewUint64(999)
				entries[2].Value, err = schema.EncodeRow(table, row)
				require.NoError(t, err)
			case "trailing primary key":
				entries[2].Key = append(entries[2].Key, 0)
			case "missing row":
				entries = append(entries[:2], entries[3:]...)
			case "stale index", "extra index suffix", "unknown index":
				row[2] = recordpkg.NewBytes([]byte("stale"))
				extra, err := schema.EncodeIndexEntry(table, 9, row)
				require.NoError(t, err)

				if scenario == "extra index suffix" {
					extra.Key = append(plan.Indexes[2].Key, 0)
				}

				if scenario == "unknown index" {
					binary.BigEndian.PutUint32(extra.Key[5:9], 999)
				}

				entries = append(entries, Entry{Key: extra.Key, Value: extra.Value})
			case "uncataloged table":
				entries = entries[2:]
			case "short key":
				entries = append(entries, Entry{Key: []byte{1}})
			case "zero table":
				entries = append(entries, Entry{Key: make([]byte, 9)})
				entries[len(entries)-1].Key[0] = 1
			case "unique violation":
				row[1] = recordpkg.NewUint64(999)
				second, err := PlanRowInsert(table, row)
				require.NoError(t, err)
				entries = append(entries, second.Primary)
			case "wrong table reference":
				other := cloneRowPlanTable(table)
				other.ID, other.Name = 2, "other"
				otherEntries := validationEntries(t, other, row)
				entries = append(entries, otherEntries...)
				extra := Entry{Key: append(bytes.Clone(plan.Indexes[2].Key), 0), Value: otherEntries[2].Key}
				entries = append(entries, extra)
			}

			path := writeValidationEntries(t, entries)
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			require.ErrorIs(t, Validate(path), ErrInvalidData)
			assertFileContents(t, path, before)
		})
	}
}

func TestValidate_ChecksBeyondScanLimit(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, _ := rowInsertFixture()
	table.Indexes = nil
	require.NoError(t, db.CreateTable(table))

	for id := uint64(1); id <= MaxScanEntries+1; id++ {
		require.NoError(t, db.InsertRow(table, numberedRow(id)))
	}

	key, err := schema.EncodePrimaryKey(table, recordpkg.NewUint64(MaxScanEntries+1))
	require.NoError(t, err)
	require.NoError(t, db.Put(key, []byte{0xff}))
	require.NoError(t, db.Close())
	require.ErrorIs(t, Validate(path), ErrInvalidData)
}

func TestValidateContentsPropagatesReadFailure(t *testing.T) {
	db, _ := openWriterDatabase(t)
	store := &hookedStore{pageStore: db.pager, read: func(uint64) ([]byte, error) { return nil, os.ErrPermission }}
	require.ErrorIs(t, validateContents(store, db.header), os.ErrPermission)
	store.read = func(uint64) ([]byte, error) { return nil, nil }
	require.ErrorIs(t, validateContents(store, db.header), pager.ErrPagerCorruptPage)
}
