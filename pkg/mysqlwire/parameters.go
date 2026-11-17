package mysqlwire

import (
	"encoding/binary"
	"math"
	"slices"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

type parameterType struct{ kind, flags byte }

func decodeExecute(data []byte, statement *preparedStatement) ([]record.Value, []parameterType, error) {
	if len(data) < 10 || data[0] != 0x17 || data[5] != 0 || binary.LittleEndian.Uint32(data[6:10]) != 1 {
		return nil, nil, errProtocol
	}

	count := len(statement.description.Parameters)

	remaining := data[10:]
	if count == 0 {
		if len(remaining) != 0 {
			return nil, nil, errProtocol
		}

		return nil, nil, nil
	}

	bitmapSize := (count + 7) / 8
	if len(remaining) < bitmapSize+1 {
		return nil, nil, errProtocol
	}

	bitmap := remaining[:bitmapSize]
	fresh := remaining[bitmapSize]
	remaining = remaining[bitmapSize+1:]
	types := slices.Clone(statement.types)

	switch fresh {
	case 0:
		if len(types) != count {
			return nil, nil, errProtocol
		}
	case 1:
		if len(remaining) < 2*count {
			return nil, nil, errProtocol
		}

		types = make([]parameterType, count)
		for i := range count {
			types[i] = parameterType{remaining[2*i], remaining[2*i+1]}
			if types[i].flags != 0 && types[i].flags != 128 {
				return nil, nil, errProtocol
			}
		}

		remaining = remaining[2*count:]
	default:
		return nil, nil, errProtocol
	}

	values := make([]record.Value, count)

	for i, typ := range types {
		if bitmap[i/8]&(1<<uint(i%8)) != 0 || typ.kind == 6 {
			values[i] = record.NewNull()
			continue
		}

		if data, ok := statement.longData[i]; ok {
			if !stringParameter(typ.kind) {
				return nil, nil, errProtocol
			}

			value, err := record.NewText(string(data))
			if err != nil {
				return nil, nil, &Error{1366, "HY000", "Invalid UTF-8 parameter"}
			}

			values[i] = value
			continue
		}

		value, rest, err := decodeParameter(remaining, typ)
		if err != nil {
			return nil, nil, err
		}

		values[i] = value
		remaining = rest
	}

	if len(remaining) != 0 {
		return nil, nil, errProtocol
	}

	return values, types, nil
}

func stringParameter(kind byte) bool {
	switch kind {
	case 15, 249, 250, 251, 252, 253, 254:
		return true
	default:
		return false
	}
}

func decodeParameter(data []byte, typ parameterType) (record.Value, []byte, error) {
	if stringParameter(typ.kind) {
		length, rest, err := readNumber(data)
		if err != nil || length > uint64(len(rest)) {
			return record.Value{}, nil, errProtocol
		}

		value, err := record.NewText(string(rest[:length]))
		if err != nil {
			return record.Value{}, nil, &Error{1366, "HY000", "Invalid UTF-8 parameter"}
		}

		return value, rest[length:], nil
	}

	size := 0

	switch typ.kind {
	case 1:
		size = 1
	case 2:
		size = 2
	case 3, 9:
		size = 4
	case 8:
		size = 8
	default:
		return record.Value{}, nil, &Error{1235, "42000", "Parameter type is not supported"}
	}

	if len(data) < size {
		return record.Value{}, nil, errProtocol
	}

	var bits uint64

	for i := range size {
		bits |= uint64(data[i]) << uint(8*i)
	}

	if typ.flags == 128 {
		return record.NewUint64(bits), data[size:], nil
	}

	if size < 8 && bits&(uint64(1)<<uint(size*8-1)) != 0 {
		bits |= math.MaxUint64 << uint(size*8)
	}

	value := int64(bits) // #nosec G115 -- Interpret the sign-extended two's-complement wire bits, including negative values.
	return record.NewInt64(value), data[size:], nil
}

func readNumber(data []byte) (uint64, []byte, error) {
	if len(data) == 0 {
		return 0, nil, errProtocol
	}

	switch data[0] {
	case 0xfc:
		if len(data) < 3 {
			return 0, nil, errProtocol
		}

		return uint64(binary.LittleEndian.Uint16(data[1:3])), data[3:], nil
	case 0xfd:
		if len(data) < 4 {
			return 0, nil, errProtocol
		}

		return uint64(data[1]) | uint64(data[2])<<8 | uint64(data[3])<<16, data[4:], nil
	case 0xfe:
		if len(data) < 9 {
			return 0, nil, errProtocol
		}

		return binary.LittleEndian.Uint64(data[1:9]), data[9:], nil
	case 0xfb, 0xff:
		return 0, nil, errProtocol
	default:
		return uint64(data[0]), data[1:], nil
	}
}
