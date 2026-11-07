package record

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"slices"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/shopspring/decimal"
)

const (
	valueTagSize        = 1
	valueLengthSize     = 4
	maxValueDepth       = 64
	maxEncodedValueSize = math.MaxUint32
)

func EncodeValue(value Value) ([]byte, error) {
	return encodeValue(value, 0)
}

func DecodeValue(encoded []byte) (Value, error) {
	if err := checkEncodedValueSize(uint64(len(encoded))); err != nil {
		return Value{}, err
	}

	value, consumed, err := decodeValue(encoded, 0)
	if err != nil {
		return Value{}, err
	}

	if consumed != len(encoded) {
		return Value{}, fmt.Errorf("trailing bytes after value: %d", len(encoded)-consumed)
	}

	return value, nil
}

func decodeValue(encoded []byte, depth int) (Value, int, error) {
	if depth > maxValueDepth {
		return Value{}, 0, fmt.Errorf("value nesting exceeds maximum depth %d", maxValueDepth)
	}

	if len(encoded) < valueTagSize {
		return Value{}, 0, fmt.Errorf("missing value type tag")
	}

	typ := Type(encoded[0])
	switch typ {
	case TypeNull:
		return NewNull(), valueTagSize, nil
	case TypeBool:
		if len(encoded) < valueTagSize+1 {
			return Value{}, 0, fmt.Errorf("truncated boolean payload")
		}

		if encoded[1] > 1 {
			return Value{}, 0, fmt.Errorf("boolean payload must be 0 or 1")
		}

		return NewBool(encoded[1] == 1), valueTagSize + 1, nil
	case TypeInt64, TypeUint64, TypeFloat64, TypeTimestamp, TypeDuration, TypeTime:
		if len(encoded) < valueTagSize+8 {
			return Value{}, 0, fmt.Errorf("truncated 64-bit payload for type %d", typ)
		}

		bits := binary.LittleEndian.Uint64(encoded[1:9])

		var value Value

		switch typ {
		case TypeInt64:
			value = NewInt64(int64(bits))
		case TypeUint64:
			value = NewUint64(bits)
		case TypeFloat64:
			value = NewFloat64(math.Float64frombits(bits))
		case TypeTimestamp:
			value = NewTimestamp(int64(bits))
		case TypeDuration:
			value = NewDuration(int64(bits))
		case TypeTime:
			if bits >= uint64(24*time.Hour) {
				return Value{}, 0, fmt.Errorf("time must be less than 24 hours")
			}

			clock := time.Date(0, time.January, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(bits))
			value = Value{typ: TypeTime, data: clock}
		}

		return value, valueTagSize + 8, nil
	case TypeFloat32:
		if len(encoded) < valueTagSize+4 {
			return Value{}, 0, fmt.Errorf("truncated float32 payload")
		}

		bits := binary.LittleEndian.Uint32(encoded[1:5])
		return NewFloat32(math.Float32frombits(bits)), valueTagSize + 4, nil
	case TypeText, TypeBytes, TypeJSON:
		payload, consumed, err := decodeLengthPrefixed(encoded[valueTagSize:])
		if err != nil {
			return Value{}, 0, fmt.Errorf("decode type %d payload: %w", typ, err)
		}

		var value Value

		switch typ {
		case TypeText:
			value, err = NewText(string(payload))
		case TypeBytes:
			value = NewBytes(payload)
		case TypeJSON:
			value, err = NewJSON(payload)
		}

		if err != nil {
			return Value{}, 0, err
		}

		return value, valueTagSize + consumed, nil
	case TypeDate, TypeDateTime:
		return decodeCalendarValue(typ, encoded)
	case TypeUUID:
		var id uuid.UUID

		if len(encoded) < valueTagSize+len(id) {
			return Value{}, 0, fmt.Errorf("truncated UUID payload")
		}

		copy(id[:], encoded[valueTagSize:valueTagSize+len(id)])
		return Value{typ: TypeUUID, data: id}, valueTagSize + len(id), nil
	case TypeDecimal:
		return decodeDecimalValue(encoded)
	case TypeArray, TypeMap:
		return decodeCollectionValue(typ, encoded, depth)
	default:
		return Value{}, 0, fmt.Errorf("unsupported value type: %d", typ)
	}
}

func decodeLengthPrefixed(encoded []byte) ([]byte, int, error) {
	if len(encoded) < valueLengthSize {
		return nil, 0, fmt.Errorf("truncated payload length")
	}

	length := binary.LittleEndian.Uint32(encoded[:valueLengthSize])
	if uint64(length) > uint64(len(encoded)-valueLengthSize) {
		return nil, 0, fmt.Errorf("payload length %d exceeds remaining bytes", length)
	}

	end := valueLengthSize + int(length)
	return encoded[valueLengthSize:end], end, nil
}

func decodeCalendarValue(typ Type, encoded []byte) (Value, int, error) {
	size := valueTagSize + 4
	if typ == TypeDateTime {
		size += 8
	}

	if len(encoded) < size {
		return Value{}, 0, fmt.Errorf("truncated calendar payload")
	}

	year := int(binary.LittleEndian.Uint16(encoded[1:3]))
	month := time.Month(encoded[3])

	day := int(encoded[4])
	if year > 9999 || month < time.January || month > time.December || day < 1 || day > 31 {
		return Value{}, 0, fmt.Errorf("invalid calendar date")
	}

	date := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	if date.Month() != month || date.Day() != day {
		return Value{}, 0, fmt.Errorf("invalid calendar date")
	}

	if typ == TypeDateTime {
		clock := binary.LittleEndian.Uint64(encoded[5:13])
		if clock >= uint64(24*time.Hour) {
			return Value{}, 0, fmt.Errorf("datetime clock must be less than 24 hours")
		}

		date = date.Add(time.Duration(clock))
	}

	return Value{typ: typ, data: date}, size, nil
}

func decodeDecimalValue(encoded []byte) (Value, int, error) {
	const headerSize = valueTagSize + 4 + 1

	if len(encoded) < headerSize {
		return Value{}, 0, fmt.Errorf("truncated decimal header")
	}

	exponent := int32(binary.LittleEndian.Uint32(encoded[1:5]))

	sign := encoded[5]
	if sign > 1 {
		return Value{}, 0, fmt.Errorf("decimal sign must be 0 or 1")
	}

	magnitude, consumed, err := decodeLengthPrefixed(encoded[headerSize:])
	if err != nil {
		return Value{}, 0, fmt.Errorf("decode decimal magnitude: %w", err)
	}

	if len(magnitude) > 0 && magnitude[0] == 0 {
		return Value{}, 0, fmt.Errorf("decimal magnitude has a leading zero")
	}

	if len(magnitude) == 0 && sign != 0 {
		return Value{}, 0, fmt.Errorf("zero decimal must have a positive sign")
	}

	coefficient := new(big.Int).SetBytes(magnitude)
	if sign == 1 {
		coefficient.Neg(coefficient)
	}

	number := decimal.NewFromBigInt(coefficient, exponent)
	return Value{typ: TypeDecimal, data: number}, headerSize + consumed, nil
}

func decodeCollectionValue(typ Type, encoded []byte, depth int) (Value, int, error) {
	const headerSize = valueTagSize + valueLengthSize

	if len(encoded) < headerSize {
		return Value{}, 0, fmt.Errorf("truncated collection count")
	}

	count := binary.LittleEndian.Uint32(encoded[1:headerSize])

	minimumEntrySize := valueTagSize
	if typ == TypeMap {
		minimumEntrySize += valueLengthSize
	}

	if uint64(count) > uint64((len(encoded)-headerSize)/minimumEntrySize) {
		return Value{}, 0, fmt.Errorf("collection count %d exceeds remaining bytes", count)
	}

	offset := headerSize

	if typ == TypeArray {
		values := make([]Value, 0, min(int(count), 64))
		for i := range int(count) {
			value, consumed, err := decodeValue(encoded[offset:], depth+1)
			if err != nil {
				return Value{}, 0, fmt.Errorf("decode array element %d: %w", i, err)
			}

			values = append(values, value)
			offset += consumed
		}

		return Value{typ: TypeArray, data: values}, offset, nil
	}

	values := make(map[string]Value, min(int(count), 64))
	previousKey := ""

	for i := range int(count) {
		keyBytes, consumed, err := decodeLengthPrefixed(encoded[offset:])
		if err != nil {
			return Value{}, 0, fmt.Errorf("decode map key %d: %w", i, err)
		}

		if !utf8.Valid(keyBytes) {
			return Value{}, 0, fmt.Errorf("map key is not valid UTF-8")
		}

		key := string(keyBytes)
		if i > 0 && key <= previousKey {
			return Value{}, 0, fmt.Errorf("map keys must be strictly increasing")
		}

		offset += consumed

		value, consumed, err := decodeValue(encoded[offset:], depth+1)
		if err != nil {
			return Value{}, 0, fmt.Errorf("decode map value for %q: %w", key, err)
		}

		values[key] = value
		previousKey = key
		offset += consumed
	}

	return Value{typ: TypeMap, data: values}, offset, nil
}

func encodeValue(value Value, depth int) ([]byte, error) {
	if depth > maxValueDepth {
		return nil, fmt.Errorf("value nesting exceeds maximum depth %d", maxValueDepth)
	}

	switch value.Type() {
	case TypeNull:
		if value.data != nil {
			return nil, fmt.Errorf("null value has a non-nil payload")
		}

		return []byte{byte(TypeNull)}, nil
	case TypeBool:
		boolVal, err := value.Bool()
		if err != nil {
			return nil, err
		}

		encoded := []byte{byte(TypeBool), 0}
		if boolVal {
			encoded[1] = 1
		}

		return encoded, nil
	case TypeInt64:
		intVal, err := value.Int64()
		if err != nil {
			return nil, err
		}

		return encodeUint64Payload(TypeInt64, uint64(intVal)), nil
	case TypeUint64:
		uintVal, err := value.Uint64()
		if err != nil {
			return nil, err
		}

		return encodeUint64Payload(TypeUint64, uintVal), nil
	case TypeFloat64:
		number, err := value.Float64()
		if err != nil {
			return nil, err
		}

		return encodeUint64Payload(TypeFloat64, math.Float64bits(number)), nil
	case TypeFloat32:
		number, err := value.Float32()
		if err != nil {
			return nil, err
		}

		encoded := make([]byte, valueTagSize+4)
		encoded[0] = byte(TypeFloat32)
		binary.LittleEndian.PutUint32(encoded[1:], math.Float32bits(number))
		return encoded, nil
	case TypeText:
		text, err := value.Text()
		if err != nil {
			return nil, err
		}

		if !utf8.ValidString(text) {
			return nil, fmt.Errorf("invalid UTF-8 text")
		}

		return appendLengthPrefixed([]byte{byte(TypeText)}, []byte(text))
	case TypeBytes:
		data, err := value.Bytes()
		if err != nil {
			return nil, err
		}

		return appendLengthPrefixed([]byte{byte(TypeBytes)}, data)
	case TypeJSON:
		data, err := value.JSON()
		if err != nil {
			return nil, err
		}

		if !json.Valid(data) {
			return nil, fmt.Errorf("invalid JSON payload")
		}

		return appendLengthPrefixed([]byte{byte(TypeJSON)}, data)
	case TypeDate:
		date, err := value.Date()
		if err != nil {
			return nil, err
		}

		return encodeCalendarValue(TypeDate, date)
	case TypeTime:
		clock, err := value.Time()
		if err != nil {
			return nil, err
		}

		return encodeUint64Payload(TypeTime, clockNanoseconds(clock)), nil
	case TypeDateTime:
		dateTime, err := value.DateTime()
		if err != nil {
			return nil, err
		}

		return encodeCalendarValue(TypeDateTime, dateTime)
	case TypeUUID:
		id, err := value.UUID()
		if err != nil {
			return nil, err
		}

		encoded := make([]byte, valueTagSize+len(id))
		encoded[0] = byte(TypeUUID)
		copy(encoded[1:], id[:])
		return encoded, nil
	case TypeDecimal:
		number, err := value.Decimal()
		if err != nil {
			return nil, err
		}

		encoded := make([]byte, valueTagSize+4+1)
		encoded[0] = byte(TypeDecimal)
		binary.LittleEndian.PutUint32(encoded[1:5], uint32(number.Exponent()))

		if number.Sign() < 0 {
			encoded[5] = 1
		}

		return appendLengthPrefixed(encoded, number.Coefficient().Bytes())
	case TypeTimestamp:
		timestamp, err := value.Timestamp()
		if err != nil {
			return nil, err
		}

		return encodeUint64Payload(TypeTimestamp, uint64(timestamp)), nil
	case TypeDuration:
		duration, err := value.Duration()
		if err != nil {
			return nil, err
		}

		return encodeUint64Payload(TypeDuration, uint64(duration)), nil
	case TypeArray:
		values, err := value.Array()
		if err != nil {
			return nil, err
		}

		encoded, err := encodeCollectionHeader(TypeArray, len(values))
		if err != nil {
			return nil, err
		}

		for i, element := range values {
			child, err := encodeValue(element, depth+1)
			if err != nil {
				return nil, fmt.Errorf("encode array element %d: %w", i, err)
			}

			encoded, err = appendEncodedBytes(encoded, child)
			if err != nil {
				return nil, err
			}
		}

		return encoded, nil
	case TypeMap:
		values, err := value.Map()
		if err != nil {
			return nil, err
		}

		encoded, err := encodeCollectionHeader(TypeMap, len(values))
		if err != nil {
			return nil, err
		}

		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}

		slices.Sort(keys)

		for _, key := range keys {
			if !utf8.ValidString(key) {
				return nil, fmt.Errorf("map key is not valid UTF-8")
			}

			encoded, err = appendLengthPrefixed(encoded, []byte(key))
			if err != nil {
				return nil, err
			}

			child, err := encodeValue(values[key], depth+1)
			if err != nil {
				return nil, fmt.Errorf("encode map value for %q: %w", key, err)
			}

			encoded, err = appendEncodedBytes(encoded, child)
			if err != nil {
				return nil, err
			}
		}

		return encoded, nil
	default:
		return nil, fmt.Errorf("unsupported value type: %v", value.Type())
	}
}

func encodeUint64Payload(typ Type, payload uint64) []byte {
	encoded := make([]byte, valueTagSize+8)
	encoded[0] = byte(typ)
	binary.LittleEndian.PutUint64(encoded[1:], payload)

	return encoded
}

func encodeCalendarValue(typ Type, value time.Time) ([]byte, error) {
	year, month, day := value.Date()
	if year < 0 || year > 9999 {
		return nil, fmt.Errorf("calendar year must be between 0 and 9999")
	}

	size := valueTagSize + 4
	if typ == TypeDateTime {
		size += 8
	}

	encoded := make([]byte, size)
	encoded[0] = byte(typ)
	binary.LittleEndian.PutUint16(encoded[1:3], uint16(year))

	encoded[3], encoded[4] = byte(month), byte(day)
	if typ == TypeDateTime {
		binary.LittleEndian.PutUint64(encoded[5:], clockNanoseconds(value))
	}

	return encoded, nil
}

func clockNanoseconds(value time.Time) uint64 {
	seconds := value.Hour()*3600 + value.Minute()*60 + value.Second()
	return uint64(seconds)*1_000_000_000 + uint64(value.Nanosecond())
}

func encodeCollectionHeader(typ Type, count int) ([]byte, error) {
	if count < 0 || uint64(count) > math.MaxUint32 {
		return nil, fmt.Errorf("collection count exceeds uint32 range")
	}

	encoded := make([]byte, valueTagSize+valueLengthSize)
	encoded[0] = byte(typ)
	binary.LittleEndian.PutUint32(encoded[1:], uint32(count))
	return encoded, nil
}

func checkEncodedValueSize(size uint64) error {
	if size > maxEncodedValueSize || size > uint64(math.MaxInt) {
		return fmt.Errorf("encoded value size %d exceeds supported limit", size)
	}

	return nil
}

func appendLengthPrefixed(encoded, payload []byte) ([]byte, error) {
	size := uint64(len(encoded)) + valueLengthSize + uint64(len(payload))
	if err := checkEncodedValueSize(size); err != nil {
		return nil, err
	}

	encoded = binary.LittleEndian.AppendUint32(encoded, uint32(len(payload)))
	return append(encoded, payload...), nil
}

func appendEncodedBytes(encoded, payload []byte) ([]byte, error) {
	if err := checkEncodedValueSize(uint64(len(encoded)) + uint64(len(payload))); err != nil {
		return nil, err
	}

	return append(encoded, payload...), nil
}
