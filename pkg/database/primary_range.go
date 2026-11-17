package database

import (
	"bytes"
	"fmt"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

type KeyBound struct {
	Value     recordpkg.Value
	Inclusive bool
}

func (db *Database) ScanRowsRangePage(table schema.Table, lower, upper *KeyBound, limit int, cursor *ScanCursor) (RowPage, error) {
	start, err := schema.EncodePrimaryPrefix(table)
	if err != nil {
		return RowPage{}, fmt.Errorf("encode primary prefix: %w", err)
	}

	end := prefixEnd(start)
	if lower != nil {
		start, err = schema.EncodePrimaryKey(table, lower.Value)
		if err != nil {
			return RowPage{}, fmt.Errorf("encode lower bound: %w", err)
		}

		if !lower.Inclusive {
			start = append(start, 0)
		}
	}

	if upper != nil {
		end, err = schema.EncodePrimaryKey(table, upper.Value)
		if err != nil {
			return RowPage{}, fmt.Errorf("encode upper bound: %w", err)
		}

		if upper.Inclusive {
			end = append(end, 0)
		}
	}

	if bytes.Compare(start, end) > 0 {
		start = bytes.Clone(end)
	}

	return db.scanPrimaryRowsPage(table, start, end, limit, cursor)
}
