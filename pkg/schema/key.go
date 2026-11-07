package schema

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

const (
	keyFormatVersion   byte   = 1
	primaryIndexID     uint32 = 0
	indexKeyHeaderSize        = 9
)

func EncodePrimaryKey(table Table, value record.Value) ([]byte, error) {
	if err := ValidateTable(table); err != nil {
		return nil, err
	}

	for _, column := range table.Columns {
		if column.ID != table.PrimaryKeyColumnID {
			continue
		}

		if value.IsNull() {
			return nil, fmt.Errorf("table %q: primary key column %q must not be null", table.Name, column.Name)
		}

		if value.Type() != column.Type {
			return nil, fmt.Errorf("table %q: primary key column %q has value type 0x%02x; expected 0x%02x", table.Name, column.Name, value.Type(), column.Type)
		}

		component, err := record.EncodeKeyValue(value)
		if err != nil {
			return nil, fmt.Errorf("encode primary key column %q: %w", column.Name, err)
		}

		return encodeIndexKey(table.ID, primaryIndexID, component)
	}

	return nil, fmt.Errorf("table %q: primary key column ID %d does not exist", table.Name, table.PrimaryKeyColumnID)
}

func EncodeIndexEntry(table Table, indexID uint32, row Row) (IndexEntry, error) {
	if err := ValidateRow(table, row); err != nil {
		return IndexEntry{}, err
	}

	var index *Index

	for i := range table.Indexes {
		if table.Indexes[i].ID == indexID {
			index = &table.Indexes[i]
			break
		}
	}

	if index == nil {
		return IndexEntry{}, fmt.Errorf("table %q: secondary index ID %d does not exist", table.Name, indexID)
	}

	primaryComponent, err := record.EncodeKeyValue(row[table.PrimaryKeyColumnID])
	if err != nil {
		return IndexEntry{}, fmt.Errorf("encode primary key: %w", err)
	}

	primaryKey, err := encodeIndexKey(table.ID, primaryIndexID, primaryComponent)
	if err != nil {
		return IndexEntry{}, err
	}

	return encodeIndexEntry(table.ID, *index, row, primaryComponent, primaryKey)
}

func EncodeIndexEntries(table Table, row Row) ([]IndexEntry, error) {
	if err := ValidateRow(table, row); err != nil {
		return nil, err
	}

	if len(table.Indexes) == 0 {
		return nil, nil
	}

	primaryComponent, err := record.EncodeKeyValue(row[table.PrimaryKeyColumnID])
	if err != nil {
		return nil, fmt.Errorf("encode primary key: %w", err)
	}

	primaryKey, err := encodeIndexKey(table.ID, primaryIndexID, primaryComponent)
	if err != nil {
		return nil, err
	}

	entries := make([]IndexEntry, 0, len(table.Indexes))

	for _, index := range table.Indexes {
		entry, err := encodeIndexEntry(table.ID, index, row, primaryComponent, bytes.Clone(primaryKey))
		if err != nil {
			return nil, fmt.Errorf("secondary index ID %d: %w", index.ID, err)
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

func encodeIndexEntry(tableID uint32, index Index, row Row, primaryComponent, primaryKey []byte) (IndexEntry, error) {
	key, err := encodeIndexKey(tableID, index.ID, nil)
	if err != nil {
		return IndexEntry{}, err
	}

	hasNull := false

	for _, id := range index.ColumnIDs {
		value := row[id]
		component := []byte{byte(record.TypeNull)}

		if value.IsNull() {
			hasNull = true
		} else {
			component, err = record.EncodeKeyValue(value)
			if err != nil {
				return IndexEntry{}, fmt.Errorf("encode index %q column ID %d: %w", index.Name, id, err)
			}
		}

		key, err = appendIndexKey(key, component)
		if err != nil {
			return IndexEntry{}, err
		}
	}

	requiresUniqueCheck := index.Unique && !hasNull
	if !requiresUniqueCheck {
		key, err = appendIndexKey(key, primaryComponent)
		if err != nil {
			return IndexEntry{}, err
		}
	}

	return IndexEntry{Key: key, Value: primaryKey, RequiresUniqueCheck: requiresUniqueCheck}, nil
}

func encodeIndexKey(tableID, indexID uint32, components []byte) ([]byte, error) {
	size := uint64(indexKeyHeaderSize) + uint64(len(components))
	if err := checkIndexKeySize(size); err != nil {
		return nil, err
	}

	encoded := make([]byte, indexKeyHeaderSize, size)
	encoded[0] = keyFormatVersion
	binary.BigEndian.PutUint32(encoded[1:5], tableID)
	binary.BigEndian.PutUint32(encoded[5:9], indexID)
	return append(encoded, components...), nil
}

func appendIndexKey(encoded, component []byte) ([]byte, error) {
	if err := checkIndexKeySize(uint64(len(encoded)) + uint64(len(component))); err != nil {
		return nil, err
	}

	return append(encoded, component...), nil
}

func checkIndexKeySize(size uint64) error {
	if size > math.MaxUint32 || size > uint64(math.MaxInt) {
		return fmt.Errorf("encoded index key size %d exceeds supported limit", size)
	}

	return nil
}
