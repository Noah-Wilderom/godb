package database

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/Noah-Wilderom/godb/pkg/pager"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

type RowInsertPlan struct {
	Primary Entry
	Indexes []schema.IndexEntry
}

func PlanRowInsert(table schema.Table, row schema.Row) (RowInsertPlan, error) {
	if len(table.Indexes) >= MaxBatchEntries {
		return RowInsertPlan{}, fmt.Errorf("got %d secondary indexes, maximum is %d for one row insert: %w", len(table.Indexes), MaxBatchEntries-1, ErrBatchTooLarge)
	}

	value, err := schema.EncodeRow(table, row)
	if err != nil {
		return RowInsertPlan{}, fmt.Errorf("encode row: %w", err)
	}

	key, err := schema.EncodePrimaryKey(table, row[table.PrimaryKeyColumnID])
	if err != nil {
		return RowInsertPlan{}, fmt.Errorf("encode primary key: %w", err)
	}

	if err := pager.ValidateEntry(key, value); err != nil {
		return RowInsertPlan{}, fmt.Errorf("primary row entry: %w", err)
	}

	plan := RowInsertPlan{Primary: Entry{Key: key, Value: value}}
	if len(table.Indexes) == 0 {
		return plan, nil
	}

	indexes := slices.Clone(table.Indexes)
	slices.SortFunc(indexes, func(a, b schema.Index) int { return cmp.Compare(a.ID, b.ID) })
	table.Indexes = indexes

	entries, err := schema.EncodeIndexEntries(table, row)
	if err != nil {
		return RowInsertPlan{}, err
	}

	for i, entry := range entries {
		if err := pager.ValidateEntry(entry.Key, entry.Value); err != nil {
			return RowInsertPlan{}, fmt.Errorf("secondary index ID %d: %w", indexes[i].ID, err)
		}
	}

	plan.Indexes = entries
	return plan, nil
}
