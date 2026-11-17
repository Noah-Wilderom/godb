package database

import (
	"bytes"
	"fmt"

	"github.com/Noah-Wilderom/godb/pkg/pager"
	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

const (
	MaxScanEntries = 1024
)

func (db *Database) Scan(start, end []byte, limit int) ([]Entry, error) {
	if err := validateScan(start, end, limit); err != nil {
		return nil, err
	}

	db.counters.reads.Add(1)
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.scanLocked(start, end, limit)
}

func (db *Database) scanLocked(start, end []byte, limit int) (entries []Entry, err error) {
	defer func() {
		if err != nil {
			db.counters.readErrors.Add(1)
		}
	}()

	if db.closed {
		return nil, ErrClosed
	}

	if db.failed != nil {
		return nil, db.failed
	}

	if end != nil && bytes.Equal(start, end) {
		return nil, nil
	}

	pending := []uint64{db.header.RootPageID}
	visited := make(map[uint64]struct{})

	for len(pending) > 0 {
		id := pending[len(pending)-1]
		pending = pending[:len(pending)-1]

		if id == 0 || id >= db.header.PageCount {
			return nil, fmt.Errorf("scan page ID %d is out of range: %w", id, pager.ErrPagerCorruptPage)
		}

		if _, seen := visited[id]; seen {
			return nil, fmt.Errorf("scan encountered repeated page ID %d: %w", id, pager.ErrPagerCorruptPage)
		}

		visited[id] = struct{}{}

		page, err := db.readPage(id)
		if err != nil {
			return nil, fmt.Errorf("read scan page %d: %w", id, err)
		}

		switch page[0] {
		case pager.PageTypeInternal:
			node, err := pager.DecodeInternal(page)
			if err != nil {
				return nil, fmt.Errorf("decode scan page %d: %w", id, err)
			}

			children := node.ChildrenForRange(start, end)
			for i := len(children) - 1; i >= 0; i-- {
				pending = append(pending, children[i])
			}
		case pager.PageTypeLeaf:
			leaf, err := pager.DecodeLeaf(page)
			if err != nil {
				return nil, fmt.Errorf("decode scan page %d: %w", id, err)
			}

			for i := leaf.LowerBound(start); ; i++ {
				key, value, found := leaf.EntryAt(i)
				if !found || (end != nil && bytes.Compare(key, end) >= 0) {
					break
				}

				if len(entries) > 0 && bytes.Compare(entries[len(entries)-1].Key, key) >= 0 {
					return nil, fmt.Errorf("scan keys are not strictly increasing: %w", pager.ErrPagerCorruptPage)
				}

				entries = append(entries, Entry{Key: key, Value: value})
				if len(entries) == limit {
					return entries, nil
				}
			}
		default:
			return nil, fmt.Errorf("unknown scan page type %d: %w", page[0], pager.ErrPagerCorruptPage)
		}
	}

	return entries, nil
}

func (db *Database) ScanRowsByIndex(table schema.Table, indexID uint32, limit int, values ...recordpkg.Value) ([]schema.Row, error) {
	prefix, err := schema.EncodeIndexPrefix(table, indexID, values...)
	if err != nil {
		return nil, fmt.Errorf("encode index prefix: %w", err)
	}

	end := prefixEnd(prefix)
	if err := validateScan(prefix, end, limit); err != nil {
		return nil, err
	}

	db.mu.RLock()

	table, err = db.requireTableLocked(table)
	if err != nil {
		db.mu.RUnlock()
		return nil, err
	}

	stored, err := db.readIndexRangeLocked(prefix, end, limit)
	db.mu.RUnlock()

	if err != nil {
		return nil, err
	}

	return decodeIndexRows(table, indexID, stored)
}

func decodeIndexRows(table schema.Table, indexID uint32, stored []storedIndexRow) ([]schema.Row, error) {
	var rows []schema.Row

	for _, item := range stored {
		row, err := decodeStoredRow(table, item.entry.Value, item.encoded)
		if err != nil {
			return nil, err
		}

		expected, err := schema.EncodeIndexEntry(table, indexID, row)
		if err != nil {
			return nil, fmt.Errorf("verify scanned index row: %w", err)
		}

		if !bytes.Equal(expected.Key, item.entry.Key) {
			return nil, fmt.Errorf("stored row does not match scanned index ID %d", indexID)
		}

		rows = append(rows, row)
	}

	return rows, nil
}

type storedIndexRow struct {
	entry   Entry
	encoded []byte
}

func (db *Database) readIndexRangeLocked(start, end []byte, limit int) ([]storedIndexRow, error) {
	db.counters.reads.Add(1)

	entries, err := db.scanLocked(start, end, limit)
	if err != nil {
		return nil, err
	}

	return db.readIndexEntriesLocked(entries)
}

func (db *Database) readIndexEntriesLocked(entries []Entry) ([]storedIndexRow, error) {
	var stored []storedIndexRow

	for _, entry := range entries {
		db.counters.reads.Add(1)

		encoded, found, err := db.getLocked(entry.Value)
		if err != nil {
			return nil, err
		}

		if !found {
			return nil, fmt.Errorf("scanned index points to a missing primary row")
		}

		stored = append(stored, storedIndexRow{entry: entry, encoded: encoded})
	}

	return stored, nil
}

func validateScan(start, end []byte, limit int) error {
	if limit < 1 || limit > MaxScanEntries {
		return fmt.Errorf("scan limit must be between 1 and %d: %w", MaxScanEntries, ErrInvalidScan)
	}

	if end != nil && bytes.Compare(start, end) > 0 {
		return fmt.Errorf("scan start exceeds end: %w", ErrInvalidScan)
	}

	return nil
}

func prefixEnd(prefix []byte) []byte {
	end := bytes.Clone(prefix)

	for i := len(end) - 1; i >= 0; i-- {
		if end[i] != 0xff {
			end[i]++
			return end[:i+1]
		}
	}

	return nil
}
