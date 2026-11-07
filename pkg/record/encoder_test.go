package record

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type valueEncodingCase struct {
	name  string
	value Value
	hex   string
}

func valueEncodingCases(t testing.TB) []valueEncodingCase {
	t.Helper()

	must := func(v Value, err error) Value {
		t.Helper()
		require.NoError(t, err)
		return v
	}

	return []valueEncodingCase{
		{"Null", NewNull(), "00"},
		{"ZeroValue", Value{}, "00"},
		{"False", NewBool(false), "0200"},
		{"True", NewBool(true), "0201"},
		{"Int64", NewInt64(42), "012a00000000000000"},
		{"ZeroInt64", NewInt64(0), "010000000000000000"},
		{"NegativeInt64", NewInt64(-1), "01ffffffffffffffff"},
		{"MinimumInt64", NewInt64(math.MinInt64), "010000000000000080"},
		{"MaximumInt64", NewInt64(math.MaxInt64), "01ffffffffffffff7f"},
		{"Uint64", NewUint64(42), "0e2a00000000000000"},
		{"ZeroUint64", NewUint64(0), "0e0000000000000000"},
		{"MaximumUint64", NewUint64(math.MaxUint64), "0effffffffffffffff"},
		{"Float64", NewFloat64(1.5), "05000000000000f83f"},
		{"Float64NegativeZero", NewFloat64(math.Copysign(0, -1)), "050000000000000080"},
		{"Float64Infinity", NewFloat64(math.Inf(1)), "05000000000000f07f"},
		{"Float64SmallestSubnormal", NewFloat64(math.SmallestNonzeroFloat64), "050100000000000000"},
		{"Float64NaNPayload", NewFloat64(math.Float64frombits(0x7ff8000000000042)), "05420000000000f87f"},
		{"Float32", NewFloat32(-1.5), "0f0000c0bf"},
		{"Float32NegativeZero", NewFloat32(math.Float32frombits(0x80000000)), "0f00000080"},
		{"Float32SmallestSubnormal", NewFloat32(math.SmallestNonzeroFloat32), "0f01000000"},
		{"Float32NaNPayload", NewFloat32(math.Float32frombits(0x7fc00042)), "0f4200c07f"},
		{"Text", must(NewText("Aµ")), "030300000041c2b5"},
		{"EmptyText", must(NewText("")), "0300000000"},
		{"Bytes", NewBytes([]byte{0, 0xff, 1}), "040300000000ff01"},
		{"NilBytes", NewBytes(nil), "0400000000"},
		{"EmptyBytes", NewBytes([]byte{}), "0400000000"},
		{"JSON", must(NewJSON([]byte(`{"a":1}`))), "06070000007b2261223a317d"},
		{"JSONWhitespace", must(NewJSON([]byte(" null "))), "0606000000206e756c6c20"},
		{"Date", must(NewDate("2024-02-29")), "07e807021d"},
		{"YearZero", must(NewDate("0000-01-01")), "0700000101"},
		{"Year9999", must(NewDate("9999-12-31")), "070f270c1f"},
		{"LeapCentury", must(NewDate("2000-02-29")), "07d007021d"},
		{"Midnight", must(NewTime("00:00:00")), "080000000000000000"},
		{"OneSecond", must(NewTime("00:00:01")), "0800ca9a3b00000000"},
		{"TimeAllComponents", must(NewTime("01:02:03.123456789")), "08157b73db62030000"},
		{"EndOfDay", must(NewTime("23:59:59.999999999")), "08ffff4e91944e0000"},
		{"TimeNanosecond", must(NewTime("00:00:00.000000001")), "080100000000000000"},
		{"DateTimeNanosecond", must(NewDateTime("2024-02-29 00:00:00.000000001")), "09e807021d0100000000000000"},
		{"DateTimeAllComponents", must(NewDateTime("2024-02-29 01:02:03.123456789")), "09e807021d157b73db62030000"},
		{"DateTimeEndOfYear9999", must(NewDateTime("9999-12-31 23:59:59.999999999")), "090f270c1fffff4e91944e0000"},
		{"UUID", must(NewUUID("00112233-4455-6677-8899-aabbccddeeff")), "0a00112233445566778899aabbccddeeff"},
		{"Decimal", must(NewDecimal("123.45")), "0bfeffffff00020000003039"},
		{"NegativeDecimal", must(NewDecimal("-123.45")), "0bfeffffff01020000003039"},
		{"ZeroDecimalWithScale", must(NewDecimal("0.00")), "0bfeffffff0000000000"},
		{"DecimalTrailingZeros", must(NewDecimal("1.00")), "0bfeffffff000100000064"},
		{"DecimalLargeExponent", must(NewDecimal("1e1000")), "0be8030000000100000001"},
		{"Timestamp", NewTimestamp(1), "0d0100000000000000"},
		{"TimestampBeforeEpoch", NewTimestamp(-1), "0dffffffffffffffff"},
		{"TimestampMinimum", NewTimestamp(math.MinInt64), "0d0000000000000080"},
		{"TimestampMaximum", NewTimestamp(math.MaxInt64), "0dffffffffffffff7f"},
		{"Duration", NewDuration(90), "105a00000000000000"},
		{"NegativeDuration", NewDuration(-1), "10ffffffffffffffff"},
		{"DurationMinimum", NewDuration(math.MinInt64), "100000000000000080"},
		{"DurationMaximum", NewDuration(math.MaxInt64), "10ffffffffffffff7f"},
		{"Map", NewMap(map[string]Value{"z": NewNull(), "a": NewBool(true)}), "0c0200000001000000610201010000007a00"},
		{"MapEmptyKey", NewMap(map[string]Value{"": NewNull()}), "0c010000000000000000"},
		{"NilMap", NewMap(nil), "0c00000000"},
		{"EmptyMap", NewMap(map[string]Value{}), "0c00000000"},
		{"Array", NewArray([]Value{NewNull(), NewBool(true), NewInt64(-1)}), "110300000000020101ffffffffffffffff"},
		{"NilArray", NewArray(nil), "1100000000"},
		{"EmptyArray", NewArray([]Value{}), "1100000000"},
		{"NestedCollections", NewArray([]Value{NewMap(map[string]Value{"a": NewArray([]Value{NewBool(true)})})}), "11010000000c01000000010000006111010000000201"},
	}
}

func TestEncodeValue_ExactBytes(t *testing.T) {
	cases := valueEncodingCases(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expected, err := hex.DecodeString(tc.hex)
			require.NoError(t, err)
			encoded, err := EncodeValue(tc.value)
			require.NoError(t, err)
			require.Equal(t, expected, encoded)
			encoded[0] ^= 0xff
			again, err := EncodeValue(tc.value)
			require.NoError(t, err)
			assert.Equal(t, expected, again)
		})
	}
}

func TestEncodeValue_MapOrderIsDeterministic(t *testing.T) {
	keys := []string{"z", "ä", "a", "", "aa"}
	first := make(map[string]Value)
	second := make(map[string]Value)

	for i, key := range keys {
		first[key] = NewInt64(int64(i))
	}

	for i := len(keys) - 1; i >= 0; i-- {
		second[keys[i]] = NewInt64(int64(i))
	}

	expected, err := EncodeValue(NewMap(first))
	require.NoError(t, err)

	for i := 0; i < 100; i++ {
		actual, err := EncodeValue(NewMap(second))
		require.NoError(t, err)
		require.Equal(t, expected, actual)
	}
}

func TestEncodeValue_RejectsInvalidPayloadsWithoutPartialOutput(t *testing.T) {
	for typ := TypeNull; typ <= TypeArray; typ++ {
		encoded, err := EncodeValue(Value{typ: typ, data: struct{}{}})
		require.Error(t, err, "type %d", typ)
		assert.Nil(t, encoded)
	}

	cases := []struct {
		name  string
		value Value
	}{
		{"UnknownType", Value{typ: 0xff}},
		{"InvalidText", Value{typ: TypeText, data: string([]byte{0xff})}},
		{"InvalidJSON", Value{typ: TypeJSON, data: []byte("{")}},
		{"InvalidMapKey", NewMap(map[string]Value{string([]byte{0xff}): NewNull()})},
		{"InvalidArrayElement", NewArray([]Value{NewBool(true), {typ: 0xff}})},
		{"InvalidMapValue", NewMap(map[string]Value{"a": NewNull(), "z": {typ: 0xff}})},
		{"InvalidDateYear", Value{typ: TypeDate, data: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}},
		{"NegativeDateTimeYear", Value{typ: TypeDateTime, data: time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := EncodeValue(tc.value)
			require.Error(t, err)
			assert.Nil(t, encoded)
		})
	}
}

func TestEncodeValue_NestingLimit(t *testing.T) {
	for _, useMap := range []bool{false, true} {
		wrap := func(value Value) Value {
			if useMap {
				return NewMap(map[string]Value{"child": value})
			}

			return NewArray([]Value{value})
		}

		value := NewNull()
		for i := 0; i < maxValueDepth; i++ {
			value = wrap(value)
		}

		_, err := EncodeValue(value)
		require.NoError(t, err)
		encoded, err := EncodeValue(wrap(value))
		require.ErrorContains(t, err, "maximum depth")
		assert.Nil(t, encoded)
	}

	cycle := make(map[string]Value)
	value := Value{typ: TypeMap, data: cycle}
	cycle["self"] = value
	encoded, err := EncodeValue(value)
	require.ErrorContains(t, err, "maximum depth")
	assert.Nil(t, encoded)
}

func TestEncodedValueLimits(t *testing.T) {
	limit := uint64(math.MaxUint32)
	if uint64(math.MaxInt) < limit {
		limit = uint64(math.MaxInt)
	}

	require.NoError(t, checkEncodedValueSize(limit))
	require.Error(t, checkEncodedValueSize(limit+1))
	require.Error(t, checkEncodedValueSize(math.MaxUint64))
	encoded, err := encodeCollectionHeader(TypeArray, -1)
	require.Error(t, err)
	assert.Nil(t, encoded)

	if uint64(math.MaxInt) > math.MaxUint32 {
		count := uint64(math.MaxUint32) + 1
		encoded, err := encodeCollectionHeader(TypeMap, int(count))
		require.Error(t, err)
		assert.Nil(t, encoded)
	}
}

func TestDecodeValue_ExactBytesAndRoundTrip(t *testing.T) {
	for _, tc := range valueEncodingCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := hex.DecodeString(tc.hex)
			require.NoError(t, err)
			value, err := DecodeValue(encoded)
			require.NoError(t, err)
			assertDecodedValue(t, tc.value, value)
			again, err := EncodeValue(value)
			require.NoError(t, err)
			assert.Equal(t, encoded, again)

			withFollowingValue := append(bytes.Clone(encoded), byte(TypeNull))
			prefix, consumed, err := decodeValue(withFollowingValue, 0)
			require.NoError(t, err)
			assert.Equal(t, len(encoded), consumed)
			assertDecodedValue(t, tc.value, prefix)
			value, err = DecodeValue(withFollowingValue)
			require.ErrorContains(t, err, "trailing bytes")
			assert.Equal(t, Value{}, value)
		})
	}
}

func assertDecodedValue(t testing.TB, expected, actual Value) {
	t.Helper()
	require.Equal(t, expected.Type(), actual.Type())

	switch expected.Type() {
	case TypeFloat32:
		want, err := expected.Float32()
		require.NoError(t, err)
		got, err := actual.Float32()
		require.NoError(t, err)
		assert.Equal(t, math.Float32bits(want), math.Float32bits(got))
	case TypeFloat64:
		want, err := expected.Float64()
		require.NoError(t, err)
		got, err := actual.Float64()
		require.NoError(t, err)
		assert.Equal(t, math.Float64bits(want), math.Float64bits(got))
	case TypeBytes, TypeJSON:
		want, ok := expected.data.([]byte)
		require.True(t, ok)
		got, ok := actual.data.([]byte)
		require.True(t, ok)
		assert.True(t, bytes.Equal(want, got))
	case TypeDecimal:
		want, err := expected.Decimal()
		require.NoError(t, err)
		got, err := actual.Decimal()
		require.NoError(t, err)
		assert.Equal(t, want.Exponent(), got.Exponent())
		assert.Zero(t, want.Coefficient().Cmp(got.Coefficient()))
	case TypeArray:
		want, err := expected.Array()
		require.NoError(t, err)
		got, err := actual.Array()
		require.NoError(t, err)
		require.Len(t, got, len(want))

		for i := range want {
			assertDecodedValue(t, want[i], got[i])
		}
	case TypeMap:
		want, err := expected.Map()
		require.NoError(t, err)
		got, err := actual.Map()
		require.NoError(t, err)
		require.Len(t, got, len(want))

		for key, wantValue := range want {
			gotValue, exists := got[key]
			require.True(t, exists)
			assertDecodedValue(t, wantValue, gotValue)
		}
	default:
		assert.Equal(t, expected, actual)
	}
}

func TestDecodeValue_RejectsEveryTruncatedPrefix(t *testing.T) {
	for _, tc := range valueEncodingCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := hex.DecodeString(tc.hex)
			require.NoError(t, err)

			for length := range len(encoded) {
				value, consumed, err := decodeValue(encoded[:length], 0)
				require.Error(t, err, "prefix length %d", length)
				assert.Zero(t, consumed)
				assert.Equal(t, Value{}, value)
			}
		})
	}
}

func TestDecodeValue_RejectsMalformedPayloads(t *testing.T) {
	cases := []struct {
		name string
		hex  string
	}{
		{"Empty", ""},
		{"UnknownType", "ff"},
		{"InvalidText", "0301000000ff"},
		{"InvalidJSON", "06010000007b"},
		{"EmptyJSON", "0600000000"},
		{"ExcessiveTextLength", "03ffffffff"},
		{"ExcessiveBytesLength", "04ffffffff"},
		{"ExcessiveJSONLength", "06ffffffff"},
		{"InvalidYear", "0710270101"},
		{"ZeroMonth", "07e8070001"},
		{"InvalidMonth", "07e8070d01"},
		{"ZeroDay", "07e8070100"},
		{"InvalidDay", "07e8070120"},
		{"NonLeapFebruary29", "07e707021d"},
		{"February30", "07e807021e"},
		{"April31", "07e807041f"},
		{"NonLeapCentury", "076c07021d"},
		{"TimeAt24Hours", "0800004f91944e0000"},
		{"TimeOverflow", "08ffffffffffffffff"},
		{"InvalidDateTimeDate", "09e707021d0000000000000000"},
		{"InvalidDateTimeClock", "09e807021d00004f91944e0000"},
		{"DecimalSign", "0b000000000200000000"},
		{"DecimalNegativeZero", "0b000000000100000000"},
		{"DecimalLeadingZero", "0b0000000000020000000001"},
		{"DecimalZeroMagnitudeByte", "0b00000000000100000000"},
		{"DecimalExcessiveMagnitude", "0b0000000000ffffffff"},
		{"ArrayExcessiveCount", "11ffffffff"},
		{"ArrayMissingValue", "110200000000"},
		{"ArrayInvalidValue", "110200000000ff"},
		{"MapExcessiveCount", "0cffffffff"},
		{"MapMissingValue", "0c010000000100000061"},
		{"MapInvalidValue", "0c0100000000000000ff"},
		{"MapExcessiveKeyLength", "0c01000000ffffffff00"},
		{"MapInvalidKey", "0c0100000001000000ff00"},
		{"MapDuplicateKey", "0c02000000010000006100010000006100"},
		{"MapUnsortedKeys", "0c02000000010000006200010000006100"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := hex.DecodeString(tc.hex)
			require.NoError(t, err)
			value, consumed, err := decodeValue(encoded, 0)
			require.Error(t, err)
			assert.Equal(t, Value{}, value)
			assert.Zero(t, consumed)
			value, err = DecodeValue(encoded)
			require.Error(t, err)
			assert.Equal(t, Value{}, value)
		})
	}

	for payload := 2; payload <= math.MaxUint8; payload++ {
		value, err := DecodeValue([]byte{byte(TypeBool), byte(payload)})
		require.Error(t, err)
		assert.Equal(t, Value{}, value)
	}
}

func TestDecodeValue_NestingLimit(t *testing.T) {
	for _, useMap := range []bool{false, true} {
		value := NewNull()

		prefix := []byte{byte(TypeArray), 1, 0, 0, 0}
		if useMap {
			prefix = []byte{byte(TypeMap), 1, 0, 0, 0, 0, 0, 0, 0}
		}

		for range maxValueDepth {
			if useMap {
				value = NewMap(map[string]Value{"": value})
			} else {
				value = NewArray([]Value{value})
			}
		}

		encoded, err := EncodeValue(value)
		require.NoError(t, err)
		decoded, err := DecodeValue(encoded)
		require.NoError(t, err)
		assertDecodedValue(t, value, decoded)
		decoded, err = DecodeValue(append(prefix, encoded...))
		require.ErrorContains(t, err, "maximum depth")
		assert.Equal(t, Value{}, decoded)
	}
}

func TestDecodeValue_DoesNotAliasInput(t *testing.T) {
	for _, tc := range valueEncodingCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := hex.DecodeString(tc.hex)
			require.NoError(t, err)
			value, err := DecodeValue(encoded)
			require.NoError(t, err)
			clear(encoded)
			assertDecodedValue(t, tc.value, value)
		})
	}
}

func TestDecodeValue_ExtremeDecimalExponents(t *testing.T) {
	for _, exponent := range []int32{math.MinInt32, math.MaxInt32} {
		encoded := []byte{byte(TypeDecimal), 0, 0, 0, 0, 0, 1, 0, 0, 0, 1}
		binary.LittleEndian.PutUint32(encoded[1:5], uint32(exponent))
		value, err := DecodeValue(encoded)
		require.NoError(t, err)
		number, err := value.Decimal()
		require.NoError(t, err)
		assert.Equal(t, exponent, number.Exponent())
		assert.Equal(t, int64(1), number.Coefficient().Int64())
		again, err := EncodeValue(value)
		require.NoError(t, err)
		assert.Equal(t, encoded, again)
	}
}

func TestDecodeValue_LargeCollectionsAndPayloads(t *testing.T) {
	array := make([]Value, 200)

	values := make(map[string]Value, len(array))
	for i := range array {
		array[i] = NewArray([]Value{NewInt64(int64(i)), NewBytes([]byte{byte(i)})})
		values[fmt.Sprintf("%04d", i)] = array[i]
	}

	for _, expected := range []Value{NewArray(array), NewMap(values), NewBytes(bytes.Repeat([]byte{0, 0xff}, 4096))} {
		encoded, err := EncodeValue(expected)
		require.NoError(t, err)
		decoded, err := DecodeValue(encoded)
		require.NoError(t, err)
		assertDecodedValue(t, expected, decoded)
		clear(encoded)
		assertDecodedValue(t, expected, decoded)
	}
}

func FuzzDecodeValue(f *testing.F) {
	for _, tc := range valueEncodingCases(f) {
		encoded, err := hex.DecodeString(tc.hex)
		require.NoError(f, err)
		f.Add(encoded)
	}

	f.Add([]byte{0xff})
	f.Add([]byte{byte(TypeArray), 0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, encoded []byte) {
		value, err := DecodeValue(encoded)
		if err != nil {
			require.Equal(t, Value{}, value)
			return
		}

		again, err := EncodeValue(value)
		require.NoError(t, err)
		require.Equal(t, encoded, again)
	})
}

func TestEncodeValue_InvalidPayload(t *testing.T) {
	cases := []struct {
		name string
		typ  Type
	}{
		{"Int64", TypeInt64},
		{"Bool", TypeBool},
		{"Text", TypeText},
		{"Bytes", TypeBytes},
		{"Float64", TypeFloat64},
		{"JSON", TypeJSON},
		{"Date", TypeDate},
		{"Time", TypeTime},
		{"DateTime", TypeDateTime},
		{"UUID", TypeUUID},
		{"Decimal", TypeDecimal},
		{"Map", TypeMap},
		{"Timestamp", TypeTimestamp},
		{"Uint64", TypeUint64},
		{"Float32", TypeFloat32},
		{"Duration", TypeDuration},
		{"Array", TypeArray},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, payload := range []any{nil, struct{}{}} {
				got, err := EncodeValue(Value{typ: tc.typ, data: payload})
				require.Error(t, err)
				assert.Nil(t, got)
			}
		})
	}
}

func TestEncodeValue_UnknownType(t *testing.T) {
	for _, typ := range []Type{Type(0x12), Type(0xff)} {
		got, err := EncodeValue(Value{typ: typ})
		require.Error(t, err, "type %x", typ)
		assert.Nil(t, got, "type %x", typ)
	}
}

func TestEncodeValue_FloatBits(t *testing.T) {
	cases := []struct {
		name  string
		value Value
		want  []byte
	}{
		{"Float64NegativeZero", NewFloat64(math.Float64frombits(0x8000000000000000)), []byte{0x05, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x80}},
		{"Float64PositiveInf", NewFloat64(math.Inf(1)), []byte{0x05, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xf0, 0x7f}},
		{"Float64NegativeInf", NewFloat64(math.Inf(-1)), []byte{0x05, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xf0, 0xff}},
		{"Float64NaNPayload", NewFloat64(math.Float64frombits(0xfff8000000001234)), []byte{0x05, 0x34, 0x12, 0x00, 0x00, 0x00, 0x00, 0xf8, 0xff}},
		{"Float32NegativeZero", NewFloat32(math.Float32frombits(0x80000000)), []byte{0x0f, 0x00, 0x00, 0x00, 0x80}},
		{"Float32PositiveInf", NewFloat32(float32(math.Inf(1))), []byte{0x0f, 0x00, 0x00, 0x80, 0x7f}},
		{"Float32NegativeInf", NewFloat32(float32(math.Inf(-1))), []byte{0x0f, 0x00, 0x00, 0x80, 0xff}},
		{"Float32NaNPayload", NewFloat32(math.Float32frombits(0xffc01234)), []byte{0x0f, 0x34, 0x12, 0xc0, 0xff}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EncodeValue(tc.value)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestEncodeValue_LongLengths(t *testing.T) {
	text := strings.Repeat("é", 150)
	textValue, err := NewText(text)
	require.NoError(t, err)
	json := []byte("\"" + strings.Repeat("a", 298) + "\"")
	jsonValue, err := NewJSON(json)
	require.NoError(t, err)

	cases := []struct {
		name  string
		value Value
		want  []byte
	}{
		{"TextByteLength", textValue, append([]byte{0x03, 0x2c, 0x01, 0x00, 0x00}, []byte(text)...)},
		{"BytesLength", NewBytes([]byte(text)), append([]byte{0x04, 0x2c, 0x01, 0x00, 0x00}, []byte(text)...)},
		{"JSONLength", jsonValue, append([]byte{0x06, 0x2c, 0x01, 0x00, 0x00}, json...)},
		{"MapKeyByteLength", NewMap(map[string]Value{text: NewNull()}), append(append([]byte{0x0c, 0x01, 0x00, 0x00, 0x00, 0x2c, 0x01, 0x00, 0x00}, []byte(text)...), 0x00)},
		{"ArrayCount", NewArray(make([]Value, 256)), append([]byte{0x11, 0x00, 0x01, 0x00, 0x00}, make([]byte, 256)...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EncodeValue(tc.value)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestEncodeValue_NestedCollections(t *testing.T) {
	value := NewArray([]Value{NewNull(), NewMap(map[string]Value{
		"x": NewArray([]Value{NewBool(true), NewInt64(-1)}),
	})})
	want := []byte{
		0x11, 0x02, 0x00, 0x00, 0x00, 0x00,
		0x0c, 0x01, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x00, 0x00, 'x',
		0x11, 0x02, 0x00, 0x00, 0x00, 0x02, 0x01,
		0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
	}
	got, err := EncodeValue(value)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestEncodeValue_NestedErrors(t *testing.T) {
	for _, invalid := range []Value{{typ: Type(0xff)}, {typ: TypeInt64, data: "wrong"}} {
		for _, value := range []Value{
			NewArray([]Value{NewNull(), NewMap(map[string]Value{"x": invalid})}),
			NewMap(map[string]Value{"a": NewNull(), "z": NewArray([]Value{invalid})}),
		} {
			got, err := EncodeValue(value)
			require.Error(t, err)
			assert.Nil(t, got)
		}
	}
}

func TestEncodeValue_DateYearOutOfRange(t *testing.T) {
	for _, typ := range []Type{TypeDate, TypeDateTime} {
		for _, year := range []int{-1, 10000} {
			got, err := EncodeValue(Value{typ: typ, data: time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)})
			require.Error(t, err)
			assert.Nil(t, got)
		}
	}
}
