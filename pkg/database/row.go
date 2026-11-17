package database

import (
	"bytes"
	"fmt"

	"github.com/Noah-Wilderom/godb/pkg/pager"
	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func (db *Database) InsertRow(table schema.Table, row schema.Row) error {
	return db.submitWrite(func() (*writeRequest, error) { return makeRowInsertRequest(table, row) })
}

func (db *Database) GetRow(table schema.Table, primaryKey recordpkg.Value) (schema.Row, bool, error) {
	key, err := schema.EncodePrimaryKey(table, primaryKey)
	if err != nil {
		return nil, false, fmt.Errorf("encode primary key: %w", err)
	}

	db.mu.RLock()

	table, err = db.requireTableLocked(table)
	if err != nil {
		db.mu.RUnlock()
		return nil, false, err
	}

	db.counters.reads.Add(1)
	encoded, found, err := db.getLocked(key)
	db.mu.RUnlock()

	if err != nil {
		return nil, false, err
	}

	if !found {
		return nil, false, nil
	}

	row, err := decodeStoredRow(table, key, encoded)
	if err != nil {
		return nil, false, err
	}

	return row, true, nil
}

func (db *Database) GetRowByUniqueIndex(table schema.Table, indexID uint32, values ...recordpkg.Value) (schema.Row, bool, error) {
	key, err := schema.EncodeUniqueIndexKey(table, indexID, values...)
	if err != nil {
		return nil, false, fmt.Errorf("encode unique index key: %w", err)
	}

	db.mu.RLock()

	table, err = db.requireTableLocked(table)
	if err != nil {
		db.mu.RUnlock()
		return nil, false, err
	}

	primaryKey, encoded, found, err := db.readUniqueIndexRowLocked(key)
	db.mu.RUnlock()

	if err != nil {
		return nil, false, fmt.Errorf("read unique index ID %d: %w", indexID, err)
	}

	if !found {
		return nil, false, nil
	}

	row, err := decodeStoredRow(table, primaryKey, encoded)
	if err != nil {
		return nil, false, err
	}

	entry, err := schema.EncodeIndexEntry(table, indexID, row)
	if err != nil {
		return nil, false, fmt.Errorf("verify unique index row: %w", err)
	}

	if !entry.RequiresUniqueCheck || !bytes.Equal(entry.Key, key) {
		return nil, false, fmt.Errorf("stored row does not match unique index ID %d", indexID)
	}

	return row, true, nil
}

func (db *Database) readUniqueIndexRowLocked(key []byte) ([]byte, []byte, bool, error) {
	db.counters.reads.Add(1)

	primaryKey, found, err := db.getLocked(key)
	if err != nil || !found {
		return nil, nil, false, err
	}

	db.counters.reads.Add(1)

	encoded, found, err := db.getLocked(primaryKey)
	if err != nil {
		return nil, nil, false, err
	}

	if !found {
		return nil, nil, false, fmt.Errorf("secondary index points to a missing primary row")
	}

	return primaryKey, encoded, true, nil
}

func decodeStoredRow(table schema.Table, key, encoded []byte) (schema.Row, error) {
	row, err := schema.DecodeRow(table, encoded)
	if err != nil {
		return nil, fmt.Errorf("decode row: %w", err)
	}

	storedKey, err := schema.EncodePrimaryKey(table, row[table.PrimaryKeyColumnID])
	if err != nil {
		return nil, fmt.Errorf("encode stored primary key: %w", err)
	}

	if !bytes.Equal(key, storedKey) {
		return nil, fmt.Errorf("stored row primary key does not match lookup key")
	}

	return row, nil
}

func makeRowInsertRequest(table schema.Table, row schema.Row) (*writeRequest, error) {
	plan, err := PlanRowInsert(table, row)
	if err != nil {
		return nil, err
	}

	request := &writeRequest{
		entries:      make([]Entry, 1, len(plan.Indexes)+1),
		insertChecks: []int{0},
		result:       make(chan error, 1),
	}
	ownedTable := cloneTableDefinition(table)
	request.table = &ownedTable
	request.entries[0] = plan.Primary

	for _, index := range plan.Indexes {
		if index.RequiresUniqueCheck {
			request.insertChecks = append(request.insertChecks, len(request.entries))
		}

		request.entries = append(request.entries, Entry{Key: index.Key, Value: index.Value})
	}

	return request, nil
}

func (db *Database) checkInsert(request *writeRequest, leaves map[uint64]*pager.BPlusLeaf) (rejection, err error) {
	for _, i := range request.insertChecks {
		entry := request.entries[i]

		location, err := db.findLeafWithCache(entry.Key, leaves)
		if err != nil {
			return nil, fmt.Errorf("check insert entry %d: %w", i, err)
		}

		value, _, found := location.leaf.Get(entry.Key)
		if !found {
			continue
		}

		if i == 0 {
			return ErrDuplicatePrimaryKey, nil
		}

		if !bytes.Equal(value, entry.Value) {
			return fmt.Errorf("secondary entry %d: %w", i, ErrUniqueConflict), nil
		}
	}

	return nil, nil
}
