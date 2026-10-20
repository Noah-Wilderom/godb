package record

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeValue(t *testing.T) {
	must := func(v Value, err error) Value {
		t.Helper()
		require.NoError(t, err)
		return v
	}
	cases := []struct {
		name  string
		value Value
		want  []byte
	}{
		{"Null", NewNull(), []byte{0x00}},
		{"ZeroValue", Value{}, []byte{0x00}},
		{"BoolFalse", NewBool(false), []byte{0x02, 0x00}},
		{"BoolTrue", NewBool(true), []byte{0x02, 0x01}},
		{"Int64Zero", NewInt64(0), []byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}},
		{"Int64Positive", NewInt64(42), []byte{0x01, 0x2a, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}},
		{"Int64Negative", NewInt64(-42), []byte{0x01, 0xd6, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}},
		{"Int64MinusOne", NewInt64(-1), []byte{0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}},
		{"Int64Min", NewInt64(math.MinInt64), []byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x80}},
		{"Int64Max", NewInt64(math.MaxInt64), []byte{0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f}},
		{"Int64ByteOrder", NewInt64(0x0102030405060708), []byte{0x01, 0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01}},
		{"Uint64Zero", NewUint64(0), []byte{0x0e, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}},
		{"Uint64Max", NewUint64(math.MaxUint64), []byte{0x0e, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}},
		{"Uint64ByteOrder", NewUint64(0x8102030405060708), []byte{0x0e, 0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x81}},
		{"Text", must(NewText("é")), []byte{0x03, 0x02, 0x00, 0x00, 0x00, 0xc3, 0xa9}},
		{"TextEmpty", must(NewText("")), []byte{0x03, 0x00, 0x00, 0x00, 0x00}},
		{"Bytes", NewBytes([]byte{0x00, 0xff}), []byte{0x04, 0x02, 0x00, 0x00, 0x00, 0x00, 0xff}},
		{"BytesNil", NewBytes(nil), []byte{0x04, 0xff, 0xff, 0xff, 0xff}},
		{"BytesEmpty", NewBytes([]byte{}), []byte{0x04, 0x00, 0x00, 0x00, 0x00}},
		{"Float64", NewFloat64(1.5), []byte{0x05, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xf8, 0x3f}},
		{"JSON", must(NewJSON([]byte(" {\"é\":1} "))), []byte{0x06, 0x0a, 0x00, 0x00, 0x00, ' ', '{', '"', 0xc3, 0xa9, '"', ':', '1', '}', ' '}},
		{"JSONNull", must(NewJSON([]byte("null"))), []byte{0x06, 0x04, 0x00, 0x00, 0x00, 'n', 'u', 'l', 'l'}},
		{"Date", must(NewDate("2024-02-29")), []byte{0x07, 0xe8, 0x07, 0x02, 0x1d}},
		{"DateYearZero", must(NewDate("0000-01-01")), []byte{0x07, 0x00, 0x00, 0x01, 0x01}},
		{"DateYear9999", must(NewDate("9999-12-31")), []byte{0x07, 0x0f, 0x27, 0x0c, 0x1f}},
		{"Time", must(NewTime("01:02:03.000000004")), []byte{0x08, 0x04, 0xae, 0x17, 0xd4, 0x62, 0x03, 0x00, 0x00}},
		{"TimeMidnight", must(NewTime("00:00:00")), []byte{0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}},
		{"TimeLastNanosecond", must(NewTime("23:59:59.999999999")), []byte{0x08, 0xff, 0xff, 0x4e, 0x91, 0x94, 0x4e, 0x00, 0x00}},
		{"DateTime", must(NewDateTime("2024-02-29 01:02:03.000000004")), []byte{0x09, 0xe8, 0x07, 0x02, 0x1d, 0x04, 0xae, 0x17, 0xd4, 0x62, 0x03, 0x00, 0x00}},
		{"DateTimeYearZero", must(NewDateTime("0000-01-01 00:00:00.000000001")), []byte{0x09, 0x00, 0x00, 0x01, 0x01, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}},
		{"DateTimeYear9999", must(NewDateTime("9999-12-31 23:59:59.999999999")), []byte{0x09, 0x0f, 0x27, 0x0c, 0x1f, 0xff, 0xff, 0x4e, 0x91, 0x94, 0x4e, 0x00, 0x00}},
		{"UUID", must(NewUUID("00112233-4455-6677-8899-aabbccddeeff")), []byte{0x0a, 0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}},
		{"Decimal", must(NewDecimal("-123.4500")), []byte{0x0b, 0xfc, 0xff, 0xff, 0xff, 0x08, 0x00, 0x00, 0x00, '-', '1', '2', '3', '4', '5', '0', '0'}},
		{"DecimalZeroScale", must(NewDecimal("0.000")), []byte{0x0b, 0xfd, 0xff, 0xff, 0xff, 0x01, 0x00, 0x00, 0x00, '0'}},
		{"DecimalMaxExponent", Value{typ: TypeDecimal, data: decimal.New(7, math.MaxInt32)}, []byte{0x0b, 0xff, 0xff, 0xff, 0x7f, 0x01, 0x00, 0x00, 0x00, '7'}},
		{"DecimalMinExponent", Value{typ: TypeDecimal, data: decimal.New(-7, math.MinInt32)}, []byte{0x0b, 0x00, 0x00, 0x00, 0x80, 0x02, 0x00, 0x00, 0x00, '-', '7'}},
		{"MapNil", NewMap(nil), []byte{0x0c, 0xff, 0xff, 0xff, 0xff}},
		{"MapEmpty", NewMap(map[string]Value{}), []byte{0x0c, 0x00, 0x00, 0x00, 0x00}},
		{"Timestamp", NewTimestamp(-1), []byte{0x0d, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}},
		{"TimestampMin", NewTimestamp(math.MinInt64), []byte{0x0d, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x80}},
		{"Float32", NewFloat32(1.5), []byte{0x0f, 0x00, 0x00, 0xc0, 0x3f}},
		{"Duration", NewDuration(-90), []byte{0x10, 0xa6, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}},
		{"DurationMax", NewDuration(math.MaxInt64), []byte{0x10, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f}},
		{"ArrayNil", NewArray(nil), []byte{0x11, 0xff, 0xff, 0xff, 0xff}},
		{"ArrayEmpty", NewArray([]Value{}), []byte{0x11, 0x00, 0x00, 0x00, 0x00}},
		{"Array", NewArray([]Value{NewBool(true), NewNull()}), []byte{0x11, 0x02, 0x00, 0x00, 0x00, 0x02, 0x01, 0x00}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EncodeValue(tc.value)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestEncodeValue_InvalidPayload(t *testing.T) {
	cases := []struct {
		name string
		typ  Type
	}{
		{"Int64", TypeInt64}, {"Bool", TypeBool}, {"Text", TypeText},
		{"Bytes", TypeBytes}, {"Float64", TypeFloat64}, {"JSON", TypeJSON},
		{"Date", TypeDate}, {"Time", TypeTime}, {"DateTime", TypeDateTime},
		{"UUID", TypeUUID}, {"Decimal", TypeDecimal}, {"Map", TypeMap},
		{"Timestamp", TypeTimestamp}, {"Uint64", TypeUint64},
		{"Float32", TypeFloat32}, {"Duration", TypeDuration}, {"Array", TypeArray},
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

func TestEncodeValue_MapsSortRawKeys(t *testing.T) {
	value := NewMap(map[string]Value{
		"\xff": NewBytes(nil), "a": NewBool(true),
		"": NewNull(), "\x00": NewBool(false),
	})
	want := []byte{
		0x0c, 0x04, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x00, 0x00, 0x00, 0x02, 0x00,
		0x01, 0x00, 0x00, 0x00, 'a', 0x02, 0x01,
		0x01, 0x00, 0x00, 0x00, 0xff, 0x04, 0xff, 0xff, 0xff, 0xff,
	}
	for range 20 {
		got, err := EncodeValue(value)
		require.NoError(t, err)
		assert.Equal(t, want, got)
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

func TestEncodeValue_NestingLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		wrap func(Value) Value
	}{
		{"Array", func(v Value) Value { return NewArray([]Value{v}) }},
		{"Map", func(v Value) Value { return NewMap(map[string]Value{"": v}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := NewNull()
			for range 256 {
				value = tc.wrap(value)
			}
			got, err := EncodeValue(value)
			require.NoError(t, err)
			assert.NotEmpty(t, got)

			got, err = EncodeValue(tc.wrap(value))
			require.Error(t, err)
			assert.Nil(t, got)
		})
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
