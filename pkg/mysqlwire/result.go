package mysqlwire

import (
	"encoding/binary"
	"strconv"

	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
	"github.com/Noah-Wilderom/godb/pkg/sql"
)

func columnPacket(database string, column schema.Column) ([]byte, error) {
	payload := appendString(nil, "def")
	payload = appendString(payload, database)
	payload = appendString(payload, "")
	payload = appendString(payload, "")
	payload = appendString(payload, column.Name)
	payload = appendString(payload, column.Name)
	payload = append(payload, 12)
	charset := uint16(63)
	length := uint32(20)

	var (
		typ   byte
		flags uint16
	)

	if !column.Nullable {
		flags |= 1
	}

	switch column.Type {
	case record.TypeInt64:
		typ = 8
	case record.TypeUint64:
		typ = 8
		flags |= 32
	case record.TypeBool:
		typ = 1
		length = 1
	case record.TypeText:
		typ = 252
		charset = 46
		length = 65535
	default:
		return nil, &Error{1235, "42000", "Column type is not supported by the MySQL endpoint"}
	}

	payload = binary.LittleEndian.AppendUint16(payload, charset)
	payload = binary.LittleEndian.AppendUint32(payload, length)
	payload = append(payload, typ)
	payload = binary.LittleEndian.AppendUint16(payload, flags)
	return append(payload, 0, 0, 0), nil
}

func textValue(value record.Value) (string, error) {
	switch value.Type() {
	case record.TypeInt64:
		number, err := value.Int64()
		return strconv.FormatInt(number, 10), err
	case record.TypeUint64:
		number, err := value.Uint64()
		return strconv.FormatUint(number, 10), err
	case record.TypeBool:
		b, err := value.Bool()
		if b {
			return "1", err
		}

		return "0", err
	case record.TypeText:
		return value.Text()
	default:
		return "", &Error{1235, "42000", "Value type is not supported by the MySQL endpoint"}
	}
}

func resultPackets(database string, result sql.Result) ([][]byte, error) {
	if len(result.Columns) > 64 {
		return nil, errProtocol
	}

	if len(result.Rows) > maxResultRows {
		return nil, resultLimit()
	}

	output := [][]byte{appendNumber(nil, uint64(len(result.Columns)))}
	size := 0

	for _, column := range result.Columns {
		payload, err := columnPacket(database, column)
		if err != nil {
			return nil, err
		}

		size += len(payload)
		if len(payload) > maxPacket || size > maxResultBytes {
			return nil, resultLimit()
		}

		output = append(output, payload)
	}

	eof := []byte{0xfe, 0, 0, 2, 0}
	output = append(output, eof)

	for _, row := range result.Rows {
		if len(row) != len(result.Columns) {
			return nil, errProtocol
		}

		var payload []byte

		for _, value := range row {
			if value.IsNull() {
				payload = append(payload, 0xfb)
				continue
			}

			str, err := textValue(value)
			if err != nil {
				return nil, err
			}

			payload = appendString(payload, str)
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
