package database

import (
	"bytes"
	"fmt"

	"github.com/Noah-Wilderom/godb/pkg/pager"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func (db *Database) UpdateRow(table schema.Table, row schema.Row) (bool, error) {
	var request *writeRequest

	err := db.submitWrite(func() (*writeRequest, error) {
		var err error

		request, err = makeRowUpdateRequest(table, row)
		return request, err
	})
	if err != nil {
		return false, err
	}

	return request.matched, nil
}

func makeRowUpdateRequest(table schema.Table, row schema.Row) (*writeRequest, error) {
	plan, err := PlanRowInsert(table, row)
	if err != nil {
		return nil, err
	}

	owned := cloneTableDefinition(table)
	return &writeRequest{
		entries: make([]Entry, min(1+2*len(plan.Indexes), MaxBatchEntries)),
		table:   &owned,
		update:  &plan,
		result:  make(chan error, 1),
	}, nil
}

func (db *Database) prepareUpdate(request *writeRequest, leaves map[uint64]*pager.BPlusLeaf) (rejection, err error) {
	next := request.update

	previous, found, err := db.readRowPlan(*request.table, next.Primary.Key, leaves)
	if err != nil {
		return nil, err
	}

	if !found {
		request.entries = nil
		return nil, nil
	}

	return db.prepareUpdatePlans(request, previous, leaves)
}

func (db *Database) prepareUpdatePlans(request *writeRequest, previous RowInsertPlan, leaves map[uint64]*pager.BPlusLeaf) (rejection, err error) {
	next := request.update
	if bytes.Equal(previous.Primary.Value, next.Primary.Value) {
		request.entries = nil
		request.matched = true
		return nil, nil
	}

	var entries []Entry

	for i, index := range previous.Indexes {
		if !bytes.Equal(index.Key, next.Indexes[i].Key) {
			entries = append(entries, Entry{Key: index.Key})
		}
	}

	removeCount := len(entries)
	if 1+2*removeCount > MaxBatchEntries {
		return fmt.Errorf("update requires %d key mutations: %w", 1+2*removeCount, ErrBatchTooLarge), nil
	}

	entries = append(entries, next.Primary)

	for i, index := range next.Indexes {
		if bytes.Equal(index.Key, previous.Indexes[i].Key) {
			continue
		}

		location, err := db.findLeafWithCache(index.Key, leaves)
		if err != nil {
			return nil, err
		}

		value, _, found := location.leaf.Get(index.Key)
		if found && !bytes.Equal(value, next.Primary.Key) {
			if index.RequiresUniqueCheck {
				return fmt.Errorf("new index entry %d: %w", i, ErrUniqueConflict), nil
			}

			return nil, fmt.Errorf("new index entry %d has an incorrect reference: %w", i, ErrInvalidData)
		}

		entries = append(entries, Entry{Key: index.Key, Value: index.Value})
	}

	request.entries = entries
	request.deletePrefix = removeCount
	request.matched = true
	return nil, nil
}
