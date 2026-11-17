package database

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"unicode/utf8"

	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func (db *Database) ListTables(after string, limit int) ([]schema.Table, error) {
	start := []byte{0, catalogTableName}

	if after != "" {
		if !utf8.ValidString(after) {
			return nil, fmt.Errorf("invalid table cursor: %w", ErrInvalidScan)
		}

		start = append(catalogNameKey(after), 0)
	}

	end := []byte{0, catalogTableName + 1}
	if err := validateScan(start, end, limit); err != nil {
		return nil, err
	}

	db.mu.RLock()
	defer db.mu.RUnlock()
	db.counters.reads.Add(1)

	entries, err := db.scanLocked(start, end, limit)
	if err != nil {
		return nil, err
	}

	tables := make([]schema.Table, 0, len(entries))
	for _, entry := range entries {
		if len(entry.Key) <= 2 || !bytes.HasPrefix(entry.Key, []byte{0, catalogTableName}) || len(entry.Value) != 6 || entry.Value[0] != 0 || entry.Value[1] != catalogTableID {
			return nil, fmt.Errorf("invalid catalog name reference: %w", ErrInvalidCatalog)
		}

		table, found, err := db.getCatalogTableLocked(binary.BigEndian.Uint32(entry.Value[2:]))
		if err != nil {
			return nil, err
		}

		if !found || table.Name != string(entry.Key[2:]) {
			return nil, fmt.Errorf("inconsistent catalog name reference: %w", ErrInvalidCatalog)
		}

		tables = append(tables, table)
	}

	return tables, nil
}
