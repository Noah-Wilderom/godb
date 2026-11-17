package database

import (
	"bytes"
	"fmt"

	"github.com/Noah-Wilderom/godb/pkg/pager"
	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func (db *Database) Delete(key []byte) (bool, error) {
	deleted, err := db.DeleteBatch([][]byte{key})
	return deleted > 0, err
}

func (db *Database) DeleteBatch(keys [][]byte) (int, error) {
	return db.submitDelete(func() (*writeRequest, error) { return makeDeleteRequest(keys) })
}

func (db *Database) DeleteRow(table schema.Table, primaryKey recordpkg.Value) (bool, error) {
	deleted, err := db.submitDelete(func() (*writeRequest, error) { return makeRowDeleteRequest(table, primaryKey) })
	return deleted > 0, err
}

func (db *Database) submitDelete(build func() (*writeRequest, error)) (int, error) {
	var request *writeRequest

	err := db.submitWrite(func() (*writeRequest, error) {
		var err error

		request, err = build()
		return request, err
	})
	if err != nil || request == nil {
		return 0, err
	}

	return request.deleted, nil
}

func makeDeleteRequest(keys [][]byte) (*writeRequest, error) {
	if len(keys) > MaxBatchEntries {
		return nil, fmt.Errorf("got %d delete keys, maximum is %d: %w", len(keys), MaxBatchEntries, ErrBatchTooLarge)
	}

	entries := make([]Entry, len(keys))
	for i, key := range keys {
		entries[i].Key = key
	}

	request, err := makeWriteRequest(entries)
	if err != nil || request == nil {
		return request, err
	}

	request.delete = true
	return request, nil
}

func makeRowDeleteRequest(table schema.Table, primaryKey recordpkg.Value) (*writeRequest, error) {
	if len(table.Indexes) >= MaxBatchEntries {
		return nil, ErrBatchTooLarge
	}

	key, err := schema.EncodePrimaryKey(table, primaryKey)
	if err != nil {
		return nil, err
	}

	if err := pager.ValidateEntry(key, nil); err != nil {
		return nil, err
	}

	owned := cloneTableDefinition(table)
	request := &writeRequest{
		entries: make([]Entry, 1+len(table.Indexes)),
		table:   &owned,
		delete:  true,
		result:  make(chan error, 1),
	}
	request.entries[0].Key = key
	return request, nil
}

func makeCountedRowDeleteRequest(table schema.Table, primaryKey recordpkg.Value) (*writeRequest, error) {
	if !primaryKey.IsNull() {
		request, err := makeRowDeleteRequest(table, primaryKey)
		if err != nil {
			return nil, err
		}

		request.countDeletedRows = true
		return request, nil
	}

	if err := schema.ValidateTable(table); err != nil {
		return nil, err
	}

	if len(table.Indexes) >= MaxBatchEntries {
		return nil, ErrBatchTooLarge
	}

	owned := cloneTableDefinition(table)
	return &writeRequest{table: &owned, entries: make([]Entry, 1+len(table.Indexes)), delete: true, countDeletedRows: true, result: make(chan error, 1)}, nil
}

func (db *Database) prepareDelete(request *writeRequest, leaves map[uint64]*pager.BPlusLeaf) error {
	if request.table == nil {
		return nil
	}

	if request.countDeletedRows {
		request.affectedRows, request.matchedRows = 0, 0
		if request.entries[0].Key == nil {
			request.entries = nil
			return nil
		}
	}

	plan, found, err := db.readRowPlan(*request.table, request.entries[0].Key, leaves)
	if err != nil {
		return err
	}

	if !found {
		request.entries = nil
		return nil
	}

	if request.countDeletedRows {
		request.affectedRows, request.matchedRows = 1, 1
	}

	for i, index := range plan.Indexes {
		request.entries[i+1] = Entry{Key: index.Key}
	}

	return nil
}

func (db *Database) readRowPlan(table schema.Table, key []byte, leaves map[uint64]*pager.BPlusLeaf) (RowInsertPlan, bool, error) {
	location, err := db.findLeafWithCache(key, leaves)
	if err != nil {
		return RowInsertPlan{}, false, err
	}

	encoded, _, found := location.leaf.Get(key)
	if !found {
		return RowInsertPlan{}, false, nil
	}

	row, err := decodeStoredRow(table, key, encoded)
	if err != nil {
		return RowInsertPlan{}, false, fmt.Errorf("stored row: %w: %w", ErrInvalidData, err)
	}

	plan, err := PlanRowInsert(table, row)
	if err != nil {
		return RowInsertPlan{}, false, fmt.Errorf("derive stored keys: %w: %w", ErrInvalidData, err)
	}

	for _, index := range plan.Indexes {
		location, err := db.findLeafWithCache(index.Key, leaves)
		if err != nil {
			return RowInsertPlan{}, false, err
		}

		value, _, found := location.leaf.Get(index.Key)
		if !found || !bytes.Equal(value, key) {
			return RowInsertPlan{}, false, fmt.Errorf("missing or incorrect index entry %x: %w", index.Key, ErrInvalidData)
		}
	}

	return plan, true, nil
}

func (db *Database) applyDelete(key []byte, leaves map[uint64]*pager.BPlusLeaf) (bool, error) {
	location, err := db.findLeafWithCache(key, leaves)
	if err != nil {
		return false, err
	}

	if !location.leaf.Delete(key) {
		return false, nil
	}

	page, err := pager.EncodeLeaf(location.leaf)
	if err != nil {
		return false, err
	}

	if err := db.writePage(location.pageID, page); err != nil {
		return false, err
	}

	return true, nil
}
