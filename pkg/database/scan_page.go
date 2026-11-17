package database

import (
	"bytes"
	"cmp"
	"fmt"
	"slices"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

type ScanCursor struct {
	database *Database
	start    []byte
	end      []byte
	lastKey  []byte
	scope    *indexScanScope
}

type EntryPage struct {
	Entries []Entry
	Next    *ScanCursor
}

type RowPage struct {
	Rows []schema.Row
	Next *ScanCursor
}

type indexScanColumn struct {
	id       uint32
	typ      recordpkg.Type
	nullable bool
}

type indexScanScope struct {
	primaryID uint32
	unique    bool
	columns   []indexScanColumn
	indexed   []uint32
}

func (db *Database) ScanPage(start, end []byte, limit int, cursor *ScanCursor) (EntryPage, error) {
	if err := validateScan(start, end, limit); err != nil {
		return EntryPage{}, err
	}

	if err := db.validateCursor(cursor, start, end, nil); err != nil {
		return EntryPage{}, err
	}

	db.counters.reads.Add(1)
	db.mu.RLock()
	defer db.mu.RUnlock()

	entries, more, err := db.scanPageLocked(start, end, limit, cursor)
	if err != nil {
		return EntryPage{}, err
	}

	page := EntryPage{Entries: entries}
	if more {
		page.Next = db.newScanCursor(start, end, entries[len(entries)-1].Key, nil)
	}

	return page, nil
}

func (db *Database) ScanRowsPage(table schema.Table, limit int, cursor *ScanCursor) (RowPage, error) {
	return db.ScanRowsRangePage(table, nil, nil, limit, cursor)
}

func (db *Database) scanPrimaryRowsPage(table schema.Table, start, end []byte, limit int, cursor *ScanCursor) (RowPage, error) {
	var err error

	if err := validateScan(start, end, limit); err != nil {
		return RowPage{}, err
	}

	scope := newIndexScanScope(table, 0)
	if err := db.validateCursor(cursor, start, end, scope); err != nil {
		return RowPage{}, err
	}

	db.mu.RLock()

	table, err = db.requireTableLocked(table)
	if err != nil {
		db.mu.RUnlock()
		return RowPage{}, err
	}

	db.counters.reads.Add(1)
	entries, more, err := db.scanPageLocked(start, end, limit, cursor)
	db.mu.RUnlock()

	if err != nil {
		return RowPage{}, err
	}

	page := RowPage{Rows: make([]schema.Row, 0, len(entries))}
	for _, entry := range entries {
		row, err := decodeStoredRow(table, entry.Key, entry.Value)
		if err != nil {
			return RowPage{}, fmt.Errorf("decode scanned primary row: %w: %w", ErrInvalidData, err)
		}

		page.Rows = append(page.Rows, row)
	}

	if more {
		page.Next = db.newScanCursor(start, end, entries[len(entries)-1].Key, scope)
	}

	return page, nil
}

func (db *Database) ScanRowsByIndexPage(table schema.Table, indexID uint32, limit int, cursor *ScanCursor, values ...recordpkg.Value) (RowPage, error) {
	start, err := schema.EncodeIndexPrefix(table, indexID, values...)
	if err != nil {
		return RowPage{}, fmt.Errorf("encode index prefix: %w", err)
	}

	return db.scanIndexRowsPage(table, indexID, start, prefixEnd(start), limit, cursor)
}

func (db *Database) scanIndexRowsPage(table schema.Table, indexID uint32, start, end []byte, limit int, cursor *ScanCursor) (RowPage, error) {
	var err error

	if err := validateScan(start, end, limit); err != nil {
		return RowPage{}, err
	}

	scope := newIndexScanScope(table, indexID)
	if err := db.validateCursor(cursor, start, end, scope); err != nil {
		return RowPage{}, err
	}

	db.mu.RLock()

	table, err = db.requireTableLocked(table)
	if err != nil {
		db.mu.RUnlock()
		return RowPage{}, err
	}

	stored, more, err := db.readIndexPageLocked(start, end, limit, cursor)
	db.mu.RUnlock()

	if err != nil {
		return RowPage{}, err
	}

	rows, err := decodeIndexRows(table, indexID, stored)
	if err != nil {
		return RowPage{}, err
	}

	page := RowPage{Rows: rows}
	if more {
		page.Next = db.newScanCursor(start, end, stored[len(stored)-1].entry.Key, scope)
	}

	return page, nil
}

func (db *Database) readIndexPageLocked(start, end []byte, limit int, cursor *ScanCursor) ([]storedIndexRow, bool, error) {
	db.counters.reads.Add(1)

	entries, more, err := db.scanPageLocked(start, end, limit, cursor)
	if err != nil {
		return nil, false, err
	}

	stored, err := db.readIndexEntriesLocked(entries)
	if err != nil {
		return nil, false, err
	}

	return stored, more, nil
}

func (db *Database) scanPageLocked(start, end []byte, limit int, cursor *ScanCursor) ([]Entry, bool, error) {
	if cursor != nil {
		start = append(bytes.Clone(cursor.lastKey), 0)
	}

	entries, err := db.scanLocked(start, end, limit+1)
	if err != nil {
		return nil, false, err
	}

	if len(entries) > limit {
		return entries[:limit:limit], true, nil
	}

	return entries, false, nil
}

func (db *Database) newScanCursor(start, end, lastKey []byte, scope *indexScanScope) *ScanCursor {
	return &ScanCursor{
		database: db,
		start:    bytes.Clone(start),
		end:      bytes.Clone(end),
		lastKey:  bytes.Clone(lastKey),
		scope:    scope,
	}
}

func (db *Database) validateCursor(cursor *ScanCursor, start, end []byte, scope *indexScanScope) error {
	if cursor == nil {
		return nil
	}

	if cursor.database != db || !bytes.Equal(cursor.start, start) || !bytes.Equal(cursor.end, end) || (cursor.end == nil) != (end == nil) {
		return ErrInvalidCursor
	}

	if !sameIndexScanScope(cursor.scope, scope) {
		return ErrInvalidCursor
	}

	return nil
}

func newIndexScanScope(table schema.Table, indexID uint32) *indexScanScope {
	scope := &indexScanScope{primaryID: table.PrimaryKeyColumnID, columns: make([]indexScanColumn, 0, len(table.Columns))}

	for _, column := range table.Columns {
		scope.columns = append(scope.columns, indexScanColumn{id: column.ID, typ: column.Type, nullable: column.Nullable})
	}

	slices.SortFunc(scope.columns, func(a, b indexScanColumn) int { return cmp.Compare(a.id, b.id) })

	for _, index := range table.Indexes {
		if index.ID == indexID {
			scope.unique = index.Unique
			scope.indexed = slices.Clone(index.ColumnIDs)
			break
		}
	}

	return scope
}

func sameIndexScanScope(a, b *indexScanScope) bool {
	if a == nil || b == nil {
		return a == b
	}

	return a.primaryID == b.primaryID && a.unique == b.unique && slices.Equal(a.columns, b.columns) && slices.Equal(a.indexed, b.indexed)
}
