package database

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

const (
	MaxCommandBytes   = 1 << 20
	commandMagic      = "GODBCMD1"
	commandHeaderSize = 10
)

func EncodeCommand(command Command) ([]byte, error) {
	encoded, _, err := encodeCommand(command)
	return encoded, err
}

func encodeCommand(command Command) ([]byte, []*writeRequest, error) {
	count := len(command.Operations)
	if count == 0 || count > MaxBatchEntries {
		return nil, nil, fmt.Errorf("command requires 1..%d operations: %w", MaxBatchEntries, ErrInvalidCommand)
	}

	encoded := make([]byte, commandHeaderSize)
	copy(encoded, commandMagic)
	binary.LittleEndian.PutUint16(encoded[8:10], uint16(count))
	requests := make([]*writeRequest, 0, len(command.Operations))
	reserved := 0

	for i, operation := range command.Operations {
		op, request, err := prepareCommandOperation(operation)
		if err != nil {
			return nil, nil, fmt.Errorf("operation %d: %w: %w", i, ErrInvalidCommand, err)
		}

		reserved += len(request.entries)
		if reserved > MaxBatchEntries {
			return nil, nil, fmt.Errorf("command mutation reservation: %w: %w", ErrInvalidCommand, ErrBatchTooLarge)
		}

		table, payload, err := encodeCommandPayload(op)
		if err != nil {
			return nil, nil, fmt.Errorf("operation %d: %w: %w", i, ErrInvalidCommand, err)
		}

		tableLength, payloadLength := len(table), len(payload)
		if tableLength > MaxCommandBytes || payloadLength > MaxCommandBytes || len(encoded)+9+tableLength+payloadLength > MaxCommandBytes {
			return nil, nil, fmt.Errorf("command exceeds %d bytes: %w", MaxCommandBytes, ErrInvalidCommand)
		}

		encoded = append(encoded, byte(op.Kind))
		encoded = binary.LittleEndian.AppendUint32(encoded, uint32(tableLength))
		encoded = append(encoded, table...)
		encoded = binary.LittleEndian.AppendUint32(encoded, uint32(payloadLength))
		encoded = append(encoded, payload...)
		requests = append(requests, request)
	}

	return encoded, requests, nil
}

func encodeCommandPayload(op CommandOperation) (table, payload []byte, err error) {
	if op.Table != nil {
		table, err = json.Marshal(op.Table)
		if err != nil {
			return nil, nil, err
		}
	}

	switch op.Kind {
	case CommandPut:
		keyLength := len(op.Key)
		if keyLength > MaxCommandBytes {
			return nil, nil, ErrInvalidCommand
		}

		payload = binary.LittleEndian.AppendUint32(nil, uint32(keyLength))
		payload = append(payload, op.Key...)
		payload = append(payload, op.Value...)
	case CommandDelete:
		payload = op.Key
	case CommandInsertRow, CommandUpdateRow:
		payload, err = schema.EncodeRow(*op.Table, op.Row)
	case CommandDeleteRow, CommandDeleteRowCounted:
		payload, err = recordpkg.EncodeValue(*op.PrimaryKey)
	case CommandPatchRow:
		payload, err = encodeRowPatch(*op.PrimaryKey, op.Row)
	}

	return table, payload, err
}

func DecodeCommand(encoded []byte) (Command, error) {
	command, _, err := decodeCommand(encoded)
	return command, err
}

func decodeCommand(encoded []byte) (Command, []*writeRequest, error) {
	if len(encoded) < commandHeaderSize || len(encoded) > MaxCommandBytes || string(encoded[:8]) != commandMagic {
		return Command{}, nil, ErrInvalidCommand
	}

	count := int(binary.LittleEndian.Uint16(encoded[8:10]))
	if count == 0 || count > MaxBatchEntries {
		return Command{}, nil, ErrInvalidCommand
	}

	command := Command{Operations: make([]CommandOperation, 0, count)}

	remaining := encoded[commandHeaderSize:]
	for i := 0; i < count; i++ {
		if len(remaining) == 0 {
			return Command{}, nil, ErrInvalidCommand
		}

		kind := CommandKind(remaining[0])
		remaining = remaining[1:]

		table, err := readCommandField(&remaining)
		if err != nil {
			return Command{}, nil, err
		}

		payload, err := readCommandField(&remaining)
		if err != nil {
			return Command{}, nil, err
		}

		op, err := decodeCommandOperation(kind, table, payload)
		if err != nil {
			return Command{}, nil, fmt.Errorf("operation %d: %w: %w", i, ErrInvalidCommand, err)
		}

		command.Operations = append(command.Operations, op)
	}

	if len(remaining) != 0 {
		return Command{}, nil, ErrInvalidCommand
	}

	canonical, requests, err := encodeCommand(command)
	if err != nil {
		return Command{}, nil, err
	}

	if !bytes.Equal(canonical, encoded) {
		return Command{}, nil, fmt.Errorf("noncanonical command: %w", ErrInvalidCommand)
	}

	return command, requests, nil
}

func readCommandField(remaining *[]byte) ([]byte, error) {
	encoded := *remaining
	if len(encoded) < 4 {
		return nil, ErrInvalidCommand
	}

	length := binary.LittleEndian.Uint32(encoded[:4])
	if length > MaxCommandBytes || int(length) > len(encoded)-4 {
		return nil, ErrInvalidCommand
	}

	end := 4 + int(length)
	*remaining = encoded[end:]
	return encoded[4:end], nil
}

func decodeCommandOperation(kind CommandKind, tableBytes, payload []byte) (CommandOperation, error) {
	op := CommandOperation{Kind: kind}

	if len(tableBytes) != 0 {
		table, err := decodeCatalogTable(append([]byte{catalogVersion}, tableBytes...))
		if err != nil {
			return CommandOperation{}, err
		}

		op.Table = &table
	}

	if (kind >= CommandCreateTable && kind <= CommandDeleteRowCounted) != (op.Table != nil) {
		return CommandOperation{}, ErrInvalidCommand
	}

	switch kind {
	case CommandPut:
		key, err := readCommandField(&payload)
		if err != nil {
			return CommandOperation{}, err
		}

		op.Key, op.Value = bytes.Clone(key), bytes.Clone(payload)
	case CommandDelete:
		op.Key = bytes.Clone(payload)
	case CommandCreateTable:
		if len(payload) != 0 {
			return CommandOperation{}, ErrInvalidCommand
		}
	case CommandInsertRow, CommandUpdateRow:
		row, err := schema.DecodeRow(*op.Table, payload)
		if err != nil {
			return CommandOperation{}, err
		}

		op.Row = row
	case CommandDeleteRow, CommandDeleteRowCounted:
		key, err := recordpkg.DecodeValue(payload)
		if err != nil {
			return CommandOperation{}, err
		}

		op.PrimaryKey = &key
	case CommandPatchRow:
		key, changes, err := decodeRowPatch(payload)
		if err != nil {
			return CommandOperation{}, err
		}

		op.PrimaryKey, op.Row = &key, changes
	default:
		return CommandOperation{}, ErrInvalidCommand
	}

	return op, nil
}
