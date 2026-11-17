package database

import (
	"fmt"
	"slices"

	"github.com/Noah-Wilderom/godb/pkg/pager"
	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func (tx *WriteTx) Get(key []byte) (value []byte, found bool, err error) {
	err = tx.withRead(func(view *Database) error {
		value, found, err = view.Get(key)
		return err
	})
	return value, found, err
}

func (tx *WriteTx) GetTable(id uint32) (table schema.Table, found bool, err error) {
	err = tx.withRead(func(view *Database) error {
		table, found, err = view.GetTable(id)
		return err
	})
	return table, found, err
}

func (tx *WriteTx) GetTableByName(name string) (table schema.Table, found bool, err error) {
	err = tx.withRead(func(view *Database) error {
		table, found, err = view.GetTableByName(name)
		return err
	})
	return table, found, err
}

func (tx *WriteTx) GetRow(table schema.Table, primaryKey recordpkg.Value) (row schema.Row, found bool, err error) {
	err = tx.withRead(func(view *Database) error {
		row, found, err = view.GetRow(table, primaryKey)
		return err
	})
	return row, found, err
}

func (tx *WriteTx) GetRowByUniqueIndex(table schema.Table, indexID uint32, values ...recordpkg.Value) (row schema.Row, found bool, err error) {
	err = tx.withRead(func(view *Database) error {
		row, found, err = view.GetRowByUniqueIndex(table, indexID, values...)
		return err
	})
	return row, found, err
}

func (tx *WriteTx) Scan(start, end []byte, limit int) (entries []Entry, err error) {
	err = tx.withRead(func(view *Database) error {
		entries, err = view.Scan(start, end, limit)
		return err
	})
	return entries, err
}

func (tx *WriteTx) ScanRowsByIndex(table schema.Table, indexID uint32, limit int, values ...recordpkg.Value) (rows []schema.Row, err error) {
	err = tx.withRead(func(view *Database) error {
		rows, err = view.ScanRowsByIndex(table, indexID, limit, values...)
		return err
	})
	return rows, err
}

func (tx *WriteTx) withRead(read func(*Database) error) (err error) {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if tx.finished || tx.db == nil {
		return ErrTransactionClosed
	}

	if tx.err != nil {
		return tx.err
	}

	defer func() {
		if err != nil {
			tx.err = err
			tx.view = nil
		}
	}()

	tx.db.mu.RLock()
	defer tx.db.mu.RUnlock()

	if tx.db.closed {
		return ErrClosed
	}

	if tx.db.failed != nil {
		return tx.db.failed
	}

	if tx.readRevision != nil && *tx.readRevision != tx.db.revision {
		return ErrTransactionConflict
	}

	if tx.view == nil {
		staged, err := newStagedPages(tx.db.pager)
		if err != nil {
			return err
		}

		header := *tx.db.header
		if staged.size != header.PageCount*uint64(header.PageSize) {
			return fmt.Errorf("database size changed outside writer: %w", pager.ErrPagerCorruptPage)
		}

		revision := tx.db.revision
		tx.readRevision = &revision
		tx.view = &Database{pager: staged, header: &header}
	}

	view := tx.view
	before := view.Stats()
	defer func() {
		after := view.Stats()
		tx.db.counters.reads.Add(after.Reads - before.Reads)
		tx.db.counters.readErrors.Add(after.ReadErrors - before.ReadErrors)
		tx.db.counters.pageReads.Add(after.PageReads - before.PageReads)
	}()

	if tx.viewApplied < len(tx.requests) {
		leaves := make(map[uint64]*pager.BPlusLeaf)

		for _, original := range tx.requests[tx.viewApplied:] {
			request := *original
			request.entries = slices.Clone(original.entries)

			_, _, rejection, err := view.applyWriteRequest(&request, leaves)
			if err != nil {
				return err
			}

			if rejection != nil {
				return rejection
			}

			tx.viewApplied++
		}
	}

	return read(view)
}
