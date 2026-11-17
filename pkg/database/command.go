package database

import (
	"cmp"
	"errors"
	"fmt"
	"slices"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

type CommandKind byte

const (
	CommandPut              CommandKind = 0x01
	CommandDelete           CommandKind = 0x02
	CommandCreateTable      CommandKind = 0x03
	CommandInsertRow        CommandKind = 0x04
	CommandUpdateRow        CommandKind = 0x05
	CommandDeleteRow        CommandKind = 0x06
	CommandPatchRow         CommandKind = 0x07
	CommandDeleteRowCounted CommandKind = 0x08
)

type Command struct {
	Operations []CommandOperation
}

type CommandOperation struct {
	Kind       CommandKind
	Key, Value []byte
	Table      *schema.Table
	Row        schema.Row
	PrimaryKey *recordpkg.Value
}

type CommandResultCode string

const (
	CommandOK                  CommandResultCode = "ok"
	CommandDuplicatePrimaryKey CommandResultCode = "duplicate_primary_key"
	CommandUniqueConflict      CommandResultCode = "unique_conflict"
	CommandTableIDExists       CommandResultCode = "table_id_exists"
	CommandTableNameExists     CommandResultCode = "table_name_exists"
	CommandTableNotFound       CommandResultCode = "table_not_found"
	CommandSchemaMismatch      CommandResultCode = "schema_mismatch"
	CommandMutationLimit       CommandResultCode = "mutation_limit"
	CommandRequestIDConflict   CommandResultCode = "request_id_conflict"
	CommandInvalidUpdate       CommandResultCode = "invalid_update"
)

type CommandResult struct {
	Code         CommandResultCode
	AffectedRows uint64
	MatchedRows  uint64
}

func (db *Database) ApplyCommand(encoded []byte) (CommandResult, error) {
	_, requests, err := decodeCommand(encoded)
	if err != nil {
		return CommandResult{}, err
	}

	err = db.submitWrite(func() (*writeRequest, error) {
		return &writeRequest{transaction: requests, result: make(chan error, 1)}, nil
	})

	outcome, err := commandResultForError(err)
	if err == nil && outcome.Code == CommandOK {
		for _, request := range requests {
			outcome.AffectedRows += request.affectedRows
			outcome.MatchedRows += request.matchedRows
		}
	}

	return outcome, err
}

func commandResultForError(err error) (CommandResult, error) {
	if err == nil {
		return CommandResult{Code: CommandOK}, nil
	}

	if errors.Is(err, ErrWriteFailed) {
		return CommandResult{}, err
	}

	for _, rejection := range []struct {
		err  error
		code CommandResultCode
	}{
		{ErrDuplicatePrimaryKey, CommandDuplicatePrimaryKey},
		{ErrUniqueConflict, CommandUniqueConflict},
		{ErrTableIDExists, CommandTableIDExists},
		{ErrTableNameExists, CommandTableNameExists},
		{ErrTableNotFound, CommandTableNotFound},
		{ErrSchemaMismatch, CommandSchemaMismatch},
		{ErrBatchTooLarge, CommandMutationLimit},
		{ErrInvalidUpdate, CommandInvalidUpdate},
	} {
		if errors.Is(err, rejection.err) {
			return CommandResult{Code: rejection.code}, nil
		}
	}

	return CommandResult{}, err
}

func prepareCommandOperation(op CommandOperation) (CommandOperation, *writeRequest, error) {
	row := op.Kind == CommandInsertRow || op.Kind == CommandUpdateRow || op.Kind == CommandPatchRow

	typed := op.Kind >= CommandCreateTable && op.Kind <= CommandDeleteRowCounted
	if op.Kind < CommandPut || op.Kind > CommandDeleteRowCounted ||
		(op.Table != nil) != typed || (op.Row != nil) != row ||
		(op.PrimaryKey != nil) != (op.Kind == CommandDeleteRow || op.Kind == CommandPatchRow || op.Kind == CommandDeleteRowCounted) ||
		(typed && len(op.Key) != 0) || (op.Kind != CommandPut && len(op.Value) != 0) {
		return CommandOperation{}, nil, fmt.Errorf("unexpected or missing fields for operation kind %d", op.Kind)
	}

	var (
		request *writeRequest
		err     error
	)

	if typed {
		table := cloneTableDefinition(*op.Table)
		slices.SortFunc(table.Columns, func(a, b schema.Column) int { return cmp.Compare(a.ID, b.ID) })
		slices.SortFunc(table.Indexes, func(a, b schema.Index) int { return cmp.Compare(a.ID, b.ID) })

		if len(table.Indexes) == 0 {
			table.Indexes = nil
		}

		op.Table = &table

		request, err = makeCreateTableRequest(table)
		if err != nil {
			return CommandOperation{}, nil, err
		}
	}

	switch op.Kind {
	case CommandPut:
		request, err = makeWriteRequest([]Entry{{Key: op.Key, Value: op.Value}})
	case CommandDelete:
		request, err = makeDeleteRequest([][]byte{op.Key})
	case CommandInsertRow:
		request, err = makeRowInsertRequest(*op.Table, op.Row)
	case CommandUpdateRow:
		request, err = makeRowUpdateRequest(*op.Table, op.Row)
	case CommandDeleteRow:
		request, err = makeRowDeleteRequest(*op.Table, *op.PrimaryKey)
	case CommandPatchRow:
		request, err = makeRowPatchRequest(*op.Table, *op.PrimaryKey, op.Row)
	case CommandDeleteRowCounted:
		request, err = makeCountedRowDeleteRequest(*op.Table, *op.PrimaryKey)
	}

	return op, request, err
}
