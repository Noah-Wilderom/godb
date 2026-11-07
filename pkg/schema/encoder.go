package schema

import (
	"encoding/binary"
	"fmt"
	"math"
	"slices"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

const (
	rowFormatVersion   byte = 1
	rowHeaderSize           = 9
	rowEntryHeaderSize      = 8
	maxEncodedRowSize       = math.MaxUint32
)

func EncodeRow(table Table, row Row) ([]byte, error) {
	if err := ValidateRow(table, row); err != nil {
		return nil, err
	}

	count := len(row)
	if uint64(count) > math.MaxUint32 {
		return nil, fmt.Errorf("row column count exceeds uint32 range")
	}

	ids := make([]uint32, 0, count)
	for id := range row {
		ids = append(ids, id)
	}

	slices.Sort(ids)
	encoded := make([]byte, rowHeaderSize)
	encoded[0] = rowFormatVersion
	binary.LittleEndian.PutUint32(encoded[1:5], table.ID)
	binary.LittleEndian.PutUint32(encoded[5:9], uint32(count))

	for _, id := range ids {
		payload, err := record.EncodeValue(row[id])
		if err != nil {
			return nil, fmt.Errorf("encode column ID %d: %w", id, err)
		}

		length := len(payload)
		if uint64(length) > math.MaxUint32 {
			return nil, fmt.Errorf("column ID %d: encoded value exceeds uint32 range", id)
		}

		if err := checkEncodedRowSize(uint64(len(encoded)) + rowEntryHeaderSize + uint64(length)); err != nil {
			return nil, err
		}

		encoded = binary.LittleEndian.AppendUint32(encoded, id)
		encoded = binary.LittleEndian.AppendUint32(encoded, uint32(length))
		encoded = append(encoded, payload...)
	}

	return encoded, nil
}

func DecodeRow(table Table, encoded []byte) (Row, error) {
	if err := ValidateTable(table); err != nil {
		return nil, err
	}

	if err := checkEncodedRowSize(uint64(len(encoded))); err != nil {
		return nil, err
	}

	if len(encoded) < rowHeaderSize {
		return nil, fmt.Errorf("truncated row header")
	}

	if encoded[0] != rowFormatVersion {
		return nil, fmt.Errorf("unsupported row format version %d", encoded[0])
	}

	tableID := binary.LittleEndian.Uint32(encoded[1:5])
	if tableID != table.ID {
		return nil, fmt.Errorf("row table ID %d does not match table ID %d", tableID, table.ID)
	}

	count := binary.LittleEndian.Uint32(encoded[5:9])
	if uint64(count) != uint64(len(table.Columns)) {
		return nil, fmt.Errorf("row column count %d does not match table column count %d", count, len(table.Columns))
	}

	remaining := encoded[rowHeaderSize:]
	if uint64(count) > uint64(len(remaining))/(rowEntryHeaderSize+1) {
		return nil, fmt.Errorf("row column count %d exceeds remaining bytes", count)
	}

	row := make(Row, min(len(table.Columns), 64))

	var previousID uint32

	for range count {
		if len(remaining) < rowEntryHeaderSize {
			return nil, fmt.Errorf("truncated row entry header")
		}

		id := binary.LittleEndian.Uint32(remaining[:4])
		if id <= previousID {
			return nil, fmt.Errorf("column IDs must be nonzero and strictly increasing: %d after %d", id, previousID)
		}

		length := binary.LittleEndian.Uint32(remaining[4:8])
		remaining = remaining[rowEntryHeaderSize:]

		if length == 0 || uint64(length) > uint64(len(remaining)) {
			return nil, fmt.Errorf("column ID %d: invalid value length %d", id, length)
		}

		value, err := record.DecodeValue(remaining[:int(length)])
		if err != nil {
			return nil, fmt.Errorf("decode column ID %d: %w", id, err)
		}

		row[id] = value
		previousID = id
		remaining = remaining[int(length):]
	}

	if len(remaining) != 0 {
		return nil, fmt.Errorf("trailing bytes after row: %d", len(remaining))
	}

	if err := ValidateRow(table, row); err != nil {
		return nil, err
	}

	return row, nil
}

func checkEncodedRowSize(size uint64) error {
	if size > maxEncodedRowSize || size > uint64(math.MaxInt) {
		return fmt.Errorf("encoded row size %d exceeds supported limit", size)
	}

	return nil
}
