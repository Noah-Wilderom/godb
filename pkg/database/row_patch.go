package database

import (
	"fmt"
	"strconv"

	"github.com/Noah-Wilderom/godb/pkg/pager"
	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

type rowPatch struct {
	key     []byte
	changes schema.Row
}

func encodeRowPatch(key recordpkg.Value, changes schema.Row) ([]byte, error) {
	values := make(map[string]recordpkg.Value, len(changes))
	for id, value := range changes {
		values[strconv.FormatUint(uint64(id), 10)] = value
	}

	return recordpkg.EncodeValue(recordpkg.NewArray([]recordpkg.Value{key, recordpkg.NewMap(values)}))
}

func decodeRowPatch(payload []byte) (recordpkg.Value, schema.Row, error) {
	value, err := recordpkg.DecodeValue(payload)
	if err != nil {
		return recordpkg.Value{}, nil, err
	}

	array, err := value.Array()
	if err != nil || len(array) != 2 {
		return recordpkg.Value{}, nil, ErrInvalidCommand
	}

	values, err := array[1].Map()
	if err != nil {
		return recordpkg.Value{}, nil, err
	}

	changes := make(schema.Row, len(values))
	for name, value := range values {
		id, err := strconv.ParseUint(name, 10, 32)
		if err != nil || strconv.FormatUint(id, 10) != name {
			return recordpkg.Value{}, nil, ErrInvalidCommand
		}

		changes[uint32(id)] = value
	}

	return array[0], changes, nil
}

func makeRowPatchRequest(table schema.Table, key recordpkg.Value, changes schema.Row) (*writeRequest, error) {
	if err := schema.ValidateTable(table); err != nil {
		return nil, err
	}

	if len(changes) == 0 || len(changes) > MaxBatchEntries || len(table.Indexes) >= MaxBatchEntries {
		return nil, ErrInvalidCommand
	}

	columns := make(map[uint32]schema.Column, len(table.Columns))
	for _, column := range table.Columns {
		columns[column.ID] = column
	}

	for id, value := range changes {
		column, exists := columns[id]
		if !exists || id == table.PrimaryKeyColumnID {
			return nil, ErrInvalidCommand
		}

		if value.IsNull() {
			if !column.Nullable {
				return nil, ErrInvalidCommand
			}
		} else if value.Type() != column.Type {
			return nil, ErrInvalidCommand
		}
	}

	var encodedKey []byte

	if !key.IsNull() {
		var err error

		encodedKey, err = schema.EncodePrimaryKey(table, key)
		if err != nil {
			return nil, err
		}
	}

	payload, err := encodeRowPatch(key, changes)
	if err != nil {
		return nil, err
	}

	_, owned, err := decodeRowPatch(payload)
	if err != nil {
		return nil, err
	}

	definition := cloneTableDefinition(table)
	return &writeRequest{table: &definition, patch: &rowPatch{key: encodedKey, changes: owned}, entries: make([]Entry, min(1+2*len(table.Indexes), MaxBatchEntries)), result: make(chan error, 1)}, nil
}

func (db *Database) preparePatch(request *writeRequest, leaves map[uint64]*pager.BPlusLeaf) (rejection, err error) {
	request.entries = nil

	request.matched, request.affectedRows, request.matchedRows = false, 0, 0
	if request.patch.key == nil {
		return nil, nil
	}

	previous, found, err := db.readRowPlan(*request.table, request.patch.key, leaves)
	if err != nil || !found {
		return nil, err
	}

	row, err := schema.DecodeRow(*request.table, previous.Primary.Value)
	if err != nil {
		return nil, err
	}

	for id, value := range request.patch.changes {
		row[id] = value
	}

	next, err := PlanRowInsert(*request.table, row)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidUpdate, err), nil
	}

	request.update = &next

	rejection, err = db.prepareUpdatePlans(request, previous, leaves)
	if rejection == nil && err == nil {
		request.matchedRows = 1
		if len(request.entries) != 0 {
			request.affectedRows = 1
		}
	}

	return rejection, err
}
