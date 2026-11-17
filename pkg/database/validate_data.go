package database

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"unicode/utf8"

	"github.com/Noah-Wilderom/godb/pkg/pager"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func validateContents(p pageStore, header *pager.DatabaseHeader) error {
	tables := make(map[uint32]schema.Table)
	names := make(map[string]uint32)

	if err := walkValidationTree(p, header, func(entry Entry) error {
		return collectCatalogEntry(entry, tables, names)
	}); err != nil {
		return err
	}

	for id, table := range tables {
		if names[table.Name] != id {
			return fmt.Errorf("table %d has a missing or incorrect name reference: %w", id, ErrInvalidCatalog)
		}
	}

	for name, id := range names {
		if table, found := tables[id]; !found || table.Name != name {
			return fmt.Errorf("table name %q has a missing or incorrect definition: %w", name, ErrInvalidCatalog)
		}
	}

	db := &Database{pager: p, header: header}
	if err := db.validateReplicationMetadata(); err != nil {
		return err
	}

	return walkValidationTree(p, header, func(entry Entry) error {
		if len(entry.Key) == 0 || entry.Key[0] != 1 {
			return nil
		}

		tableID, indexID, err := schema.DecodeIndexKeyHeader(entry.Key)
		if err != nil {
			return fmt.Errorf("index key header: %w: %w", ErrInvalidData, err)
		}

		table, found := tables[tableID]
		if !found {
			return fmt.Errorf("table %d: %w: %w", tableID, ErrInvalidData, ErrTableNotFound)
		}

		if indexID == 0 {
			return db.validatePrimaryEntry(table, entry)
		}

		return db.validateSecondaryEntry(table, indexID, entry)
	})
}

func collectCatalogEntry(entry Entry, tables map[uint32]schema.Table, names map[string]uint32) error {
	key := entry.Key
	if len(key) == 0 || key[0] != 0 {
		return nil
	}

	if len(key) < 2 {
		return fmt.Errorf("truncated catalog key: %w", ErrInvalidCatalog)
	}

	switch key[1] {
	case replicationNamespace:
		return nil
	case catalogTableID:
		if len(key) != 6 {
			return fmt.Errorf("invalid table ID key length: %w", ErrInvalidCatalog)
		}

		table, err := decodeCatalogTable(entry.Value)
		if err != nil {
			return err
		}

		if table.ID != binary.BigEndian.Uint32(key[2:]) {
			return fmt.Errorf("table ID does not match catalog key: %w", ErrInvalidCatalog)
		}

		tables[table.ID] = table
	case catalogTableName:
		name := string(key[2:])

		ref := entry.Value
		if name == "" || !utf8.ValidString(name) || len(ref) != 6 || ref[0] != 0 || ref[1] != catalogTableID {
			return fmt.Errorf("invalid table name reference: %w", ErrInvalidCatalog)
		}

		id := binary.BigEndian.Uint32(ref[2:])
		if id == 0 {
			return fmt.Errorf("zero table ID in name reference: %w", ErrInvalidCatalog)
		}

		names[name] = id
	default:
		return fmt.Errorf("unknown catalog key type %d: %w", key[1], ErrInvalidCatalog)
	}

	return nil
}

func (db *Database) validatePrimaryEntry(table schema.Table, entry Entry) error {
	row, err := decodeStoredRow(table, entry.Key, entry.Value)
	if err != nil {
		return fmt.Errorf("primary row: %w: %w", ErrInvalidData, err)
	}

	indexes, err := schema.EncodeIndexEntries(table, row)
	if err != nil {
		return fmt.Errorf("derive indexes: %w: %w", ErrInvalidData, err)
	}

	for _, index := range indexes {
		value, found, err := db.getLocked(index.Key)
		if err != nil {
			return err
		}

		if !found || !bytes.Equal(value, index.Value) {
			return fmt.Errorf("missing or incorrect secondary index entry %x: %w", index.Key, ErrInvalidData)
		}
	}

	return nil
}

func (db *Database) validateSecondaryEntry(table schema.Table, indexID uint32, entry Entry) error {
	encoded, found, err := db.getLocked(entry.Value)
	if err != nil {
		return err
	}

	if !found {
		return fmt.Errorf("index %d points to a missing primary row: %w", indexID, ErrInvalidData)
	}

	row, err := decodeStoredRow(table, entry.Value, encoded)
	if err != nil {
		return fmt.Errorf("index %d primary reference: %w: %w", indexID, ErrInvalidData, err)
	}

	expected, err := schema.EncodeIndexEntry(table, indexID, row)
	if err != nil {
		return fmt.Errorf("index %d: %w: %w", indexID, ErrInvalidData, err)
	}

	if !bytes.Equal(expected.Key, entry.Key) {
		return fmt.Errorf("index %d entry does not match its row: %w", indexID, ErrInvalidData)
	}

	return nil
}
