package record

import (
	"encoding/binary"
	"fmt"
	"maps"
	"math"
	"slices"
	"time"
)

const maxValueDepth = 256

func EncodeValue(value Value) ([]byte, error) {
	return appendEncodedValue(nil, value, 0)
}

func appendEncodedValue(buffer []byte, value Value, depth int) ([]byte, error) {
	if depth > maxValueDepth {
		return nil, fmt.Errorf("value nesting exceeds maximum depth %d", maxValueDepth)
	}

	buffer = append(buffer, byte(value.Type()))
	switch v := value.Type(); v {
	case TypeNull:
		return buffer, nil
	case TypeBool:
		typeBool, err := value.Bool()
		if err != nil {
			return nil, err
		}

		if typeBool {
			return append(buffer, 0x01), nil
		}

		return append(buffer, 0x00), nil
	case TypeInt64:
		typeInt64, err := value.Int64()
		if err != nil {
			return nil, err
		}

		return binary.LittleEndian.AppendUint64(buffer, uint64(typeInt64)), nil
	case TypeUint64:
		typeUint64, err := value.Uint64()
		if err != nil {
			return nil, err
		}

		return binary.LittleEndian.AppendUint64(buffer, typeUint64), nil
	case TypeFloat64:
		number, err := value.Float64()
		if err != nil {
			return nil, err
		}

		return binary.LittleEndian.AppendUint64(buffer, math.Float64bits(number)), nil
	case TypeFloat32:
		number, err := value.Float32()
		if err != nil {
			return nil, err
		}

		return binary.LittleEndian.AppendUint32(buffer, math.Float32bits(number)), nil
	case TypeTimestamp:
		seconds, err := value.Timestamp()
		if err != nil {
			return nil, err
		}

		return binary.LittleEndian.AppendUint64(buffer, uint64(seconds)), nil
	case TypeDuration:
		seconds, err := value.Duration()
		if err != nil {
			return nil, err
		}

		return binary.LittleEndian.AppendUint64(buffer, uint64(seconds)), nil
	case TypeUUID:
		id, err := value.UUID()
		if err != nil {
			return nil, err
		}

		return append(buffer, id[:]...), nil
	case TypeText:
		text, err := value.Text()
		if err != nil {
			return nil, err
		}

		return appendValueBytes(buffer, []byte(text))
	case TypeBytes:
		data, err := value.Bytes()
		if err != nil {
			return nil, err
		}
		if data == nil {
			return binary.LittleEndian.AppendUint32(buffer, math.MaxUint32), nil
		}

		return appendValueBytes(buffer, data)
	case TypeJSON:
		data, err := value.JSON()
		if err != nil {
			return nil, err
		}

		return appendValueBytes(buffer, data)
	case TypeDate:
		date, err := value.Date()
		if err != nil {
			return nil, err
		}

		return appendValueDate(buffer, date)
	case TypeTime:
		clock, err := value.Time()
		if err != nil {
			return nil, err
		}

		return appendValueTime(buffer, clock), nil
	case TypeDateTime:
		dateTime, err := value.DateTime()
		if err != nil {
			return nil, err
		}
		buffer, err = appendValueDate(buffer, dateTime)
		if err != nil {
			return nil, err
		}

		return appendValueTime(buffer, dateTime), nil
	case TypeDecimal:
		number, err := value.Decimal()
		if err != nil {
			return nil, err
		}

		buffer = binary.LittleEndian.AppendUint32(buffer, uint32(number.Exponent()))
		return appendValueBytes(buffer, []byte(number.Coefficient().String()))
	case TypeArray:
		values, err := value.Array()
		if err != nil {
			return nil, err
		}
		if values == nil {
			return binary.LittleEndian.AppendUint32(buffer, math.MaxUint32), nil
		}
		buffer, err = appendValueLength(buffer, len(values))
		if err != nil {
			return nil, err
		}
		for i, element := range values {
			buffer, err = appendEncodedValue(buffer, element, depth+1)
			if err != nil {
				return nil, fmt.Errorf("array element %d: %w", i, err)
			}
		}

		return buffer, nil
	case TypeMap:
		values, err := value.Map()
		if err != nil {
			return nil, err
		}
		if values == nil {
			return binary.LittleEndian.AppendUint32(buffer, math.MaxUint32), nil
		}
		buffer, err = appendValueLength(buffer, len(values))
		if err != nil {
			return nil, err
		}
		for _, key := range slices.Sorted(maps.Keys(values)) {
			buffer, err = appendValueBytes(buffer, []byte(key))
			if err != nil {
				return nil, fmt.Errorf("map key %q: %w", key, err)
			}
			buffer, err = appendEncodedValue(buffer, values[key], depth+1)
			if err != nil {
				return nil, fmt.Errorf("map key %q: %w", key, err)
			}
		}

		return buffer, nil
	default:
		return nil, fmt.Errorf("unsupported value type %d", v)
	}
}

func appendValueLength(buffer []byte, length int) ([]byte, error) {
	if uint64(length) >= math.MaxUint32 {
		return nil, fmt.Errorf("value length or count %d exceeds maximum %d", length, uint64(math.MaxUint32)-1)
	}

	return binary.LittleEndian.AppendUint32(buffer, uint32(length)), nil
}

func appendValueBytes(buffer, data []byte) ([]byte, error) {
	buffer, err := appendValueLength(buffer, len(data))
	if err != nil {
		return nil, err
	}

	return append(buffer, data...), nil
}

func appendValueDate(buffer []byte, date time.Time) ([]byte, error) {
	year := date.Year()
	if year < 0 || year > 9999 {
		return nil, fmt.Errorf("date year %d is outside range 0000–9999", year)
	}

	buffer = binary.LittleEndian.AppendUint16(buffer, uint16(year))
	return append(buffer, byte(date.Month()), byte(date.Day())), nil
}

func appendValueTime(buffer []byte, clock time.Time) []byte {
	seconds := uint64(clock.Hour()*3600 + clock.Minute()*60 + clock.Second())
	nanoseconds := seconds*uint64(time.Second) + uint64(clock.Nanosecond())
	return binary.LittleEndian.AppendUint64(buffer, nanoseconds)
}
