package database

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/Noah-Wilderom/godb/pkg/pager"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

const (
	catalogVersion   byte = 1
	catalogTableID   byte = 1
	catalogTableName byte = 2
)

func (db *Database) CreateTable(table schema.Table) error {
	return db.submitWrite(func() (*writeRequest, error) { return makeCreateTableRequest(table) })
}

func (db *Database) GetTable(id uint32) (schema.Table, bool, error) {
	if id == 0 {
		return schema.Table{}, false, fmt.Errorf("table ID must be nonzero")
	}

	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.getTableLocked(id)
}

func (db *Database) getTableLocked(id uint32) (schema.Table, bool, error) {
	table, found, err := db.getCatalogTableLocked(id)
	if err != nil || !found {
		return schema.Table{}, false, err
	}

	ref, found, err := db.getCatalogEntryLocked(catalogNameKey(table.Name))
	if err != nil {
		return schema.Table{}, false, err
	}

	if !found || !bytes.Equal(ref, catalogIDKey(id)) {
		return schema.Table{}, false, fmt.Errorf("table %d has an inconsistent name reference: %w", id, ErrInvalidCatalog)
	}

	return table, true, nil
}

func (db *Database) requireTableLocked(expected schema.Table) (schema.Table, error) {
	table, found, err := db.getTableLocked(expected.ID)
	if err != nil {
		return schema.Table{}, err
	}

	if !found {
		return schema.Table{}, fmt.Errorf("table %d: %w", expected.ID, ErrTableNotFound)
	}

	if !sameTableDefinition(table, expected) {
		return schema.Table{}, fmt.Errorf("table %d: %w", expected.ID, ErrSchemaMismatch)
	}

	return table, nil
}

func sameTableDefinition(a, b schema.Table) bool {
	if a.ID != b.ID || a.Name != b.Name || a.PrimaryKeyColumnID != b.PrimaryKeyColumnID || len(a.Columns) != len(b.Columns) || len(a.Indexes) != len(b.Indexes) {
		return false
	}

	columns := make(map[uint32]schema.Column, len(a.Columns))
	for _, column := range a.Columns {
		columns[column.ID] = column
	}

	for _, column := range b.Columns {
		if stored, ok := columns[column.ID]; !ok || stored != column {
			return false
		}
	}

	indexes := make(map[uint32]schema.Index, len(a.Indexes))
	for _, index := range a.Indexes {
		indexes[index.ID] = index
	}

	for _, index := range b.Indexes {
		stored, ok := indexes[index.ID]
		if !ok || stored.Name != index.Name || stored.Unique != index.Unique || !slices.Equal(stored.ColumnIDs, index.ColumnIDs) {
			return false
		}
	}

	return true
}

func cloneTableDefinition(table schema.Table) schema.Table {
	table.Columns = slices.Clone(table.Columns)
	table.Indexes = slices.Clone(table.Indexes)

	for i := range table.Indexes {
		table.Indexes[i].ColumnIDs = slices.Clone(table.Indexes[i].ColumnIDs)
	}

	return table
}

func (db *Database) GetTableByName(name string) (schema.Table, bool, error) {
	if name == "" || !utf8.ValidString(name) {
		return schema.Table{}, false, fmt.Errorf("table name must be nonempty valid UTF-8")
	}

	key := catalogNameKey(name)
	if err := pager.ValidateEntry(key, nil); err != nil {
		return schema.Table{}, false, err
	}

	db.mu.RLock()
	defer db.mu.RUnlock()

	ref, found, err := db.getCatalogEntryLocked(key)
	if err != nil || !found {
		return schema.Table{}, false, err
	}

	if len(ref) != 6 || ref[0] != 0 || ref[1] != catalogTableID {
		return schema.Table{}, false, fmt.Errorf("table %q has an invalid ID reference: %w", name, ErrInvalidCatalog)
	}

	id := binary.BigEndian.Uint32(ref[2:])

	table, found, err := db.getCatalogTableLocked(id)
	if err != nil {
		return schema.Table{}, false, err
	}

	if !found || table.Name != name {
		return schema.Table{}, false, fmt.Errorf("table %q has an inconsistent ID reference: %w", name, ErrInvalidCatalog)
	}

	return table, true, nil
}

func makeCreateTableRequest(table schema.Table) (*writeRequest, error) {
	if err := schema.ValidateTable(table); err != nil {
		return nil, err
	}

	if len(table.Indexes) >= MaxBatchEntries {
		return nil, fmt.Errorf("table requires more than %d writes per row: %w", MaxBatchEntries, ErrBatchTooLarge)
	}

	payload, err := json.Marshal(table)
	if err != nil {
		return nil, fmt.Errorf("encode table definition: %w", err)
	}

	key := catalogIDKey(table.ID)

	request, err := makeWriteRequest([]Entry{
		{Key: key, Value: append([]byte{catalogVersion}, payload...)},
		{Key: catalogNameKey(table.Name), Value: key},
	})
	if err != nil {
		return nil, err
	}

	request.absentChecks = []absentCheck{
		{entry: 0, err: fmt.Errorf("table %d: %w", table.ID, ErrTableIDExists)},
		{entry: 1, err: fmt.Errorf("table %q: %w", table.Name, ErrTableNameExists)},
	}
	return request, nil
}

func catalogIDKey(id uint32) []byte {
	return binary.BigEndian.AppendUint32([]byte{0, catalogTableID}, id)
}

func catalogNameKey(name string) []byte {
	return append([]byte{0, catalogTableName}, name...)
}

func (db *Database) getCatalogEntryLocked(key []byte) ([]byte, bool, error) {
	db.counters.reads.Add(1)
	return db.getLocked(key)
}

func (db *Database) getCatalogTableLocked(id uint32) (schema.Table, bool, error) {
	encoded, found, err := db.getCatalogEntryLocked(catalogIDKey(id))
	if err != nil || !found {
		return schema.Table{}, false, err
	}

	table, err := decodeCatalogTable(encoded)
	if err != nil {
		return schema.Table{}, false, err
	}

	if table.ID != id {
		return schema.Table{}, false, fmt.Errorf("table definition does not match ID %d: %w", id, ErrInvalidCatalog)
	}

	return table, true, nil
}

func decodeCatalogTable(encoded []byte) (schema.Table, error) {
	if len(encoded) < 2 || encoded[0] != catalogVersion {
		return schema.Table{}, fmt.Errorf("missing or unsupported catalog version: %w", ErrInvalidCatalog)
	}

	var table schema.Table

	if err := json.Unmarshal(encoded[1:], &table); err != nil {
		return schema.Table{}, fmt.Errorf("decode table definition: %w: %w", ErrInvalidCatalog, err)
	}

	if err := schema.ValidateTable(table); err != nil {
		return schema.Table{}, fmt.Errorf("validate table definition: %w: %w", ErrInvalidCatalog, err)
	}

	payload, err := json.Marshal(table)
	if err != nil {
		return schema.Table{}, fmt.Errorf("encode table definition: %w: %w", ErrInvalidCatalog, err)
	}

	if !bytes.Equal(payload, encoded[1:]) || len(table.Indexes) >= MaxBatchEntries {
		return schema.Table{}, fmt.Errorf("noncanonical or unsupported table definition: %w", ErrInvalidCatalog)
	}

	return table, nil
}
