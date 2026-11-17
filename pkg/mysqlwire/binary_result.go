package mysqlwire

import (
	"encoding/binary"

	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/sql"
)

func binaryResultPackets(database string, result sql.Result) ([][]byte, error) {
	if len(result.Columns) > 64 {
		return nil, errProtocol
	}

	if len(result.Rows) > maxResultRows {
		return nil, resultLimit()
	}

	output := [][]byte{appendNumber(nil, uint64(len(result.Columns)))}
	size := 0

	for _, column := range result.Columns {
		data, err := columnPacket(database, column)
		if err != nil {
			return nil, err
		}

		size += len(data)
		if len(data) > maxPacket || size > maxResultBytes {
			return nil, resultLimit()
		}

		output = append(output, data)
	}

	eof := []byte{0xfe, 0, 0, 2, 0}
	output = append(output, eof)

	for _, row := range result.Rows {
		if len(row) != len(result.Columns) {
			return nil, errProtocol
		}

		payload := make([]byte, 1+(len(row)+9)/8)
		for i, value := range row {
			if value.IsNull() {
				payload[1+(i+2)/8] |= 1 << uint((i+2)%8)
				continue
			}

			if value.Type() != result.Columns[i].Type {
				return nil, errProtocol
			}

			var err error

			payload, err = appendBinaryValue(payload, value)
			if err != nil {
				return nil, err
			}

			if len(payload) > maxPacket || size+len(payload) > maxResultBytes {
				return nil, resultLimit()
			}
		}

		size += len(payload)
		if size > maxResultBytes {
			return nil, resultLimit()
		}

		output = append(output, payload)
	}

	return append(output, eof), nil
}

func appendBinaryValue(dst []byte, value record.Value) ([]byte, error) {
	switch value.Type() {
	case record.TypeInt64:
		number, err := value.Int64()
		bits := uint64(number) // #nosec G115 -- Preserve signed two's-complement bits in MySQL's binary row representation.
		return binary.LittleEndian.AppendUint64(dst, bits), err
	case record.TypeUint64:
		number, err := value.Uint64()
		return binary.LittleEndian.AppendUint64(dst, number), err
	case record.TypeBool:
		b, err := value.Bool()
		if b {
			return append(dst, 1), err
		}

		return append(dst, 0), err
	case record.TypeText:
		text, err := value.Text()
		return appendString(dst, text), err
	default:
		return nil, &Error{1235, "42000", "Binary result type is not supported"}
	}
}
