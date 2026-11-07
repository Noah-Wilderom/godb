package record

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"unicode/utf8"
)

func EncodeKeyValue(value Value) ([]byte, error) {
	switch value.Type() {
	case TypeInt64:
		number, err := value.Int64()
		if err != nil {
			return nil, err
		}

		ordered := uint64(number) ^ (uint64(1) << 63) // #nosec G115 -- Reinterpret signed bits and flip the sign bit to preserve signed numeric order.
		return binary.BigEndian.AppendUint64([]byte{byte(TypeInt64)}, ordered), nil
	case TypeUint64:
		number, err := value.Uint64()
		if err != nil {
			return nil, err
		}

		return binary.BigEndian.AppendUint64([]byte{byte(TypeUint64)}, number), nil
	case TypeText:
		text, err := value.Text()
		if err != nil {
			return nil, err
		}

		if !utf8.ValidString(text) {
			return nil, fmt.Errorf("invalid UTF-8 key text")
		}

		return encodeEscapedKeyValue(TypeText, []byte(text))
	case TypeBytes:
		data, err := value.Bytes()
		if err != nil {
			return nil, err
		}

		return encodeEscapedKeyValue(TypeBytes, data)
	case TypeUUID:
		id, err := value.UUID()
		if err != nil {
			return nil, err
		}

		return append([]byte{byte(TypeUUID)}, id[:]...), nil
	default:
		return nil, fmt.Errorf("unsupported key value type 0x%02x", value.Type())
	}
}

func encodeEscapedKeyValue(typ Type, data []byte) ([]byte, error) {
	if uint64(len(data)) > math.MaxUint32 {
		return nil, fmt.Errorf("key value length exceeds uint32 range")
	}

	size := uint64(len(data)) + uint64(bytes.Count(data, []byte{0})) + 3 // #nosec G115 -- Count with a nonempty separator is nonnegative and bounded by len(data).
	if size > math.MaxUint32 || size > uint64(math.MaxInt) {
		return nil, fmt.Errorf("encoded key value size %d exceeds supported limit", size)
	}

	encoded := make([]byte, 1, size)
	encoded[0] = byte(typ)

	for _, b := range data {
		encoded = append(encoded, b)

		if b == 0 {
			encoded = append(encoded, 0xff)
		}
	}

	return append(encoded, 0, 0), nil
}
