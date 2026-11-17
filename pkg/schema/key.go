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

func DecodeIndexKeyHeader(encoded []byte) (tableID, indexID uint32, err error) {
	if len(encoded) < indexKeyHeaderSize || encoded[0] != keyFormatVersion {
		return 0, 0, fmt.Errorf("missing or unsupported index key header")
	}

	tableID = binary.BigEndian.Uint32(encoded[1:5])
	if tableID == 0 {
		return 0, 0, fmt.Errorf("index key table ID must be nonzero")
	}

	return tableID, binary.BigEndian.Uint32(encoded[5:9]), nil
}

func EncodePrimaryPrefix(table Table) ([]byte, error) {
	if err := ValidateTable(table); err != nil {
		return nil, err
	}

	return encodeIndexKey(table.ID, primaryIndexID, nil)
}

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

	index, err := findIndex(table, indexID)
	if err != nil {
		return IndexEntry{}, err
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

func EncodeUniqueIndexKey(table Table, indexID uint32, values ...record.Value) ([]byte, error) {
	if err := ValidateTable(table); err != nil {
		return nil, err
	}

	index, err := findIndex(table, indexID)
	if err != nil {
		return nil, err
	}

	if !index.Unique {
		return nil, fmt.Errorf("index %q is not unique", index.Name)
	}

	if len(values) != len(index.ColumnIDs) {
		return nil, fmt.Errorf("index %q requires %d values, got %d", index.Name, len(index.ColumnIDs), len(values))
	}

	return encodeIndexPrefix(table, *index, false, values)
}

func EncodeIndexPrefix(table Table, indexID uint32, values ...record.Value) ([]byte, error) {
	if err := ValidateTable(table); err != nil {
		return nil, err
	}

	index, err := findIndex(table, indexID)
	if err != nil {
		return nil, err
	}

	if len(values) > len(index.ColumnIDs) {
		return nil, fmt.Errorf("index %q has %d columns, got %d prefix values", index.Name, len(index.ColumnIDs), len(values))
	}

	return encodeIndexPrefix(table, *index, true, values)
}

func encodeIndexPrefix(table Table, index Index, allowNull bool, values []record.Value) ([]byte, error) {
	columns := make(map[uint32]Column, len(table.Columns))
	for _, column := range table.Columns {
		columns[column.ID] = column
	}

	key, err := encodeIndexKey(table.ID, index.ID, nil)
	if err != nil {
		return nil, err
	}

	for i, value := range values {
		id := index.ColumnIDs[i]
		column := columns[id]
		component := []byte{byte(record.TypeNull)}

		if value.IsNull() {
			if !allowNull {
				return nil, fmt.Errorf("index %q: null column ID %d requires a scan, not a unique lookup", index.Name, id)
			}

			if !column.Nullable {
				return nil, fmt.Errorf("index %q: column ID %d must not be null", index.Name, id)
			}
		} else {
			if value.Type() != column.Type {
				return nil, fmt.Errorf("index %q: column ID %d has value type 0x%02x; expected 0x%02x", index.Name, id, value.Type(), column.Type)
			}

			component, err = record.EncodeKeyValue(value)
			if err != nil {
				return nil, fmt.Errorf("encode index %q column ID %d: %w", index.Name, id, err)
			}
		}

		key, err = appendIndexKey(key, component)
		if err != nil {
			return nil, err
		}
	}

	return key, nil
}

func findIndex(table Table, indexID uint32) (*Index, error) {
	for i := range table.Indexes {
		if table.Indexes[i].ID == indexID {
			return &table.Indexes[i], nil
		}
	}

	return nil, fmt.Errorf("table %q: secondary index ID %d does not exist", table.Name, indexID)
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
