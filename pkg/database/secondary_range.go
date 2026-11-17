package database

import (
	"bytes"
	"fmt"
	"slices"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func (db *Database) ScanRowsByIndexRangePage(table schema.Table, indexID uint32, lower, upper *KeyBound, limit int, cursor *ScanCursor, values ...recordpkg.Value) (RowPage, error) {
	prefix, err := schema.EncodeIndexPrefix(table, indexID, values...)
	if err != nil {
		return RowPage{}, fmt.Errorf("encode index prefix: %w", err)
	}

	var columnID uint32

	for _, index := range table.Indexes {
		if index.ID != indexID {
			continue
		}

		if len(values) == len(index.ColumnIDs) {
			return RowPage{}, fmt.Errorf("index range requires an unconstrained column: %w", ErrInvalidScan)
		}

		columnID = index.ColumnIDs[len(values)]
		break
	}

	start, end := prefix, prefixEnd(prefix)
	components := append(slices.Clone(values), recordpkg.NewNull())

	for _, column := range table.Columns {
		if column.ID == columnID && column.Nullable {
			nullPrefix, err := schema.EncodeIndexPrefix(table, indexID, components...)
			if err != nil {
				return RowPage{}, err
			}

			start = prefixEnd(nullPrefix)
			break
		}
	}

	if lower != nil {
		if lower.Value.IsNull() {
			return RowPage{}, fmt.Errorf("NULL lower index bound: %w", ErrInvalidScan)
		}

		components[len(values)] = lower.Value

		start, err = schema.EncodeIndexPrefix(table, indexID, components...)
		if err != nil {
			return RowPage{}, fmt.Errorf("encode lower index bound: %w", err)
		}

		if !lower.Inclusive {
			start = prefixEnd(start)
		}
	}

	if upper != nil {
		if upper.Value.IsNull() {
			return RowPage{}, fmt.Errorf("NULL upper index bound: %w", ErrInvalidScan)
		}

		components[len(values)] = upper.Value

		end, err = schema.EncodeIndexPrefix(table, indexID, components...)
		if err != nil {
			return RowPage{}, fmt.Errorf("encode upper index bound: %w", err)
		}

		if upper.Inclusive {
			end = prefixEnd(end)
		}
	}

	if bytes.Compare(start, end) > 0 {
		start = bytes.Clone(end)
	}

	return db.scanIndexRowsPage(table, indexID, start, end, limit, cursor)
}
