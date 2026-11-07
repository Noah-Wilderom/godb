package schema

import (
	"fmt"
	"unicode/utf8"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func ValidateColumns(columns []Column) error {
	if len(columns) == 0 {
		return fmt.Errorf("schema must contain at least one column")
	}

	ids := make(map[uint32]struct{}, len(columns))
	names := make(map[string]struct{}, len(columns))

	for i, column := range columns {
		if column.ID == 0 {
			return fmt.Errorf("column %d: ID must be nonzero", i)
		}

		if _, exists := ids[column.ID]; exists {
			return fmt.Errorf("column %d: duplicate ID %d", i, column.ID)
		}

		if column.Name == "" {
			return fmt.Errorf("column %d: name must not be empty", i)
		}

		if !utf8.ValidString(column.Name) {
			return fmt.Errorf("column %d: name must be valid UTF-8", i)
		}

		if _, exists := names[column.Name]; exists {
			return fmt.Errorf("column %d: duplicate name %q", i, column.Name)
		}

		switch column.Type {
		case record.TypeInt64, record.TypeBool, record.TypeText, record.TypeBytes,
			record.TypeFloat64, record.TypeJSON, record.TypeDate, record.TypeTime,
			record.TypeDateTime, record.TypeUUID, record.TypeDecimal, record.TypeMap,
			record.TypeTimestamp, record.TypeUint64, record.TypeFloat32,
			record.TypeDuration, record.TypeArray:
		case record.TypeNull:
			return fmt.Errorf("column %d (%q): TypeNull is not a column type; use Nullable", i, column.Name)
		default:
			return fmt.Errorf("column %d (%q): unsupported type 0x%02x", i, column.Name, column.Type)
		}

		ids[column.ID] = struct{}{}
		names[column.Name] = struct{}{}
	}

	return nil
}

func ValidateTable(table Table) error {
	if table.ID == 0 {
		return fmt.Errorf("table ID must be nonzero")
	}

	if table.Name == "" {
		return fmt.Errorf("table name must not be empty")
	}

	if !utf8.ValidString(table.Name) {
		return fmt.Errorf("table name must be valid UTF-8")
	}

	if err := ValidateIndexes(table.Columns, table.Indexes); err != nil {
		return fmt.Errorf("table %q: %w", table.Name, err)
	}

	if table.PrimaryKeyColumnID == 0 {
		return fmt.Errorf("table %q: primary key column ID must be nonzero", table.Name)
	}

	for _, column := range table.Columns {
		if column.ID != table.PrimaryKeyColumnID {
			continue
		}

		if column.Nullable {
			return fmt.Errorf("table %q: primary key column %q must not be nullable", table.Name, column.Name)
		}

		if !isIndexableType(column.Type) {
			return fmt.Errorf("table %q: primary key column %q has unsupported type 0x%02x", table.Name, column.Name, column.Type)
		}

		return nil
	}

	return fmt.Errorf("table %q: primary key column ID %d does not exist", table.Name, table.PrimaryKeyColumnID)
}

func ValidateRow(table Table, row Row) error {
	if err := ValidateTable(table); err != nil {
		return err
	}

	for _, column := range table.Columns {
		value, exists := row[column.ID]
		if !exists {
			return fmt.Errorf("table %q: row is missing column %q (ID %d)", table.Name, column.Name, column.ID)
		}

		if value.IsNull() {
			if !column.Nullable {
				return fmt.Errorf("table %q: column %q (ID %d) must not be null", table.Name, column.Name, column.ID)
			}

			continue
		}

		if value.Type() != column.Type {
			return fmt.Errorf("table %q: column %q (ID %d) has value type 0x%02x; expected 0x%02x", table.Name, column.Name, column.ID, value.Type(), column.Type)
		}
	}

	if len(row) != len(table.Columns) {
		return fmt.Errorf("table %q: row contains %d unknown column IDs", table.Name, len(row)-len(table.Columns))
	}

	return nil
}

func ValidateIndexes(columns []Column, indexes []Index) error {
	if err := ValidateColumns(columns); err != nil {
		return err
	}

	if len(indexes) == 0 {
		return nil
	}

	byID := make(map[uint32]Column, len(columns))
	for _, column := range columns {
		byID[column.ID] = column
	}

	ids := make(map[uint32]struct{}, len(indexes))
	names := make(map[string]struct{}, len(indexes))

	for i, index := range indexes {
		if index.ID == primaryIndexID {
			return fmt.Errorf("index %d: ID 0 is reserved for the primary index", i)
		}

		if _, exists := ids[index.ID]; exists {
			return fmt.Errorf("index %d: duplicate ID %d", i, index.ID)
		}

		if index.Name == "" {
			return fmt.Errorf("index %d: name must not be empty", i)
		}

		if !utf8.ValidString(index.Name) {
			return fmt.Errorf("index %d: name must be valid UTF-8", i)
		}

		if _, exists := names[index.Name]; exists {
			return fmt.Errorf("index %d: duplicate name %q", i, index.Name)
		}

		if len(index.ColumnIDs) == 0 {
			return fmt.Errorf("index %q: must contain at least one column ID", index.Name)
		}

		seen := make(map[uint32]struct{}, len(index.ColumnIDs))

		for _, id := range index.ColumnIDs {
			column, exists := byID[id]
			if !exists {
				return fmt.Errorf("index %q: column ID %d does not exist", index.Name, id)
			}

			if _, exists := seen[id]; exists {
				return fmt.Errorf("index %q: duplicate column ID %d", index.Name, id)
			}

			if !isIndexableType(column.Type) {
				return fmt.Errorf("index %q: column %q (ID %d) has unsupported type 0x%02x", index.Name, column.Name, id, column.Type)
			}

			seen[id] = struct{}{}
		}

		ids[index.ID] = struct{}{}
		names[index.Name] = struct{}{}
	}

	return nil
}

func isIndexableType(typ record.Type) bool {
	switch typ {
	case record.TypeInt64, record.TypeUint64, record.TypeText, record.TypeBytes, record.TypeUUID:
		return true
	default:
		return false
	}
}
