package record

import (
	"math"
	"testing"
	"time"
	"uuid"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func valueAccessor[T any](read func(Value) (T, error)) func(Value) (any, error) {
	return func(v Value) (any, error) { return read(v) }
}

func TestValue_ConstructorsAndAccessors(t *testing.T) {
	must := func(v Value, err error) Value {
		t.Helper()
		require.NoError(t, err)
		return v
	}
	cases := []struct {
		name  string
		typ   Type
		value Value
		read  func(Value) (any, error)
		want  any
	}{
		{"Int64", TypeInt64, NewInt64(math.MinInt64), valueAccessor(Value.Int64), int64(math.MinInt64)},
		{"Uint64", TypeUint64, NewUint64(math.MaxUint64), valueAccessor(Value.Uint64), uint64(math.MaxUint64)},
		{"Bool", TypeBool, NewBool(false), valueAccessor(Value.Bool), false},
		{"Text", TypeText, must(NewText("Noah 🌳")), valueAccessor(Value.Text), "Noah 🌳"},
		{"Bytes", TypeBytes, NewBytes([]byte{0, 0xff}), valueAccessor(Value.Bytes), []byte{0, 0xff}},
		{"Float64", TypeFloat64, NewFloat64(-123.5), valueAccessor(Value.Float64), -123.5},
		{"Float32", TypeFloat32, NewFloat32(-123.5), valueAccessor(Value.Float32), float32(-123.5)},
		{"JSON", TypeJSON, must(NewJSON([]byte(`{"a":null}`))), valueAccessor(Value.JSON), []byte(`{"a":null}`)},
		{"Date", TypeDate, must(NewDate("2024-02-29")), valueAccessor(Value.Date), time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)},
		{"Time", TypeTime, must(NewTime("23:59:59")), valueAccessor(Value.Time), time.Date(0, 1, 1, 23, 59, 59, 0, time.UTC)},
		{"DateTime", TypeDateTime, must(NewDateTime("2024-02-29 23:59:59")), valueAccessor(Value.DateTime), time.Date(2024, 2, 29, 23, 59, 59, 0, time.UTC)},
		{"UUID", TypeUUID, must(NewUUID("00112233-4455-6677-8899-aabbccddeeff")), valueAccessor(Value.UUID), uuid.UUID{0, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}},
		{"Decimal", TypeDecimal, must(NewDecimal("-123.4500")), valueAccessor(Value.Decimal), decimal.New(-1234500, -4)},
		{"Timestamp", TypeTimestamp, NewTimestamp(-1), valueAccessor(Value.Timestamp), int64(-1)},
		{"Duration", TypeDuration, NewDuration(-90), valueAccessor(Value.Duration), int64(-90)},
		{"Map", TypeMap, NewMap(map[string]Value{"answer": NewInt64(42)}), valueAccessor(Value.Map), map[string]Value{"answer": NewInt64(42)}},
		{"Array", TypeArray, NewArray([]Value{NewNull(), NewBool(true)}), valueAccessor(Value.Array), []Value{NewNull(), NewBool(true)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.typ, tc.value.Type())
			assert.False(t, tc.value.IsNull())
			got, err := tc.read(tc.value)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			for _, other := range cases {
				if other.typ == tc.typ {
					continue
				}
				_, err := tc.read(other.value)
				require.Error(t, err, "must reject %s", other.name)
			}
			for _, invalid := range []Value{NewNull(), {typ: tc.typ, data: nil}, {typ: tc.typ, data: struct{}{}}} {
				_, err := tc.read(invalid)
				require.Error(t, err)
			}
		})
	}
}

func TestValue_NullAndEmptyValuesRemainDistinct(t *testing.T) {
	assert.Equal(t, NewNull(), Value{})
	assert.True(t, Value{}.IsNull())
	assert.Equal(t, TypeNull, Value{}.Type())
	text, err := NewText("")
	require.NoError(t, err)
	jsonNull, err := NewJSON([]byte("null"))
	require.NoError(t, err)
	for _, value := range []Value{text, jsonNull, NewBytes(nil), NewBytes([]byte{}), NewMap(nil), NewArray(nil), NewBool(false), NewInt64(0)} {
		assert.False(t, value.IsNull())
	}
	nilBytes, err := NewBytes(nil).Bytes()
	require.NoError(t, err)
	assert.Nil(t, nilBytes)
	emptyBytes, err := NewBytes([]byte{}).Bytes()
	require.NoError(t, err)
	assert.NotNil(t, emptyBytes)
	assert.Empty(t, emptyBytes)
	nilMap, err := NewMap(nil).Map()
	require.NoError(t, err)
	assert.Nil(t, nilMap)
	nilArray, err := NewArray(nil).Array()
	require.NoError(t, err)
	assert.Nil(t, nilArray)
}

func TestValue_InvalidConstructorInputReturnsZeroValue(t *testing.T) {
	cases := []struct {
		name   string
		create func() (Value, error)
	}{
		{"InvalidUTF8", func() (Value, error) { return NewText(string([]byte{0xff})) }},
		{"EmptyJSON", func() (Value, error) { return NewJSON(nil) }},
		{"TruncatedJSON", func() (Value, error) { return NewJSON([]byte(`{"a":`)) }},
		{"MultipleJSONValues", func() (Value, error) { return NewJSON([]byte(`{} []`)) }},
		{"NonLeapDate", func() (Value, error) { return NewDate("2023-02-29") }},
		{"InvalidMonth", func() (Value, error) { return NewDate("2024-13-01") }},
		{"InvalidHour", func() (Value, error) { return NewTime("24:00:00") }},
		{"InvalidMinute", func() (Value, error) { return NewTime("12:60:00") }},
		{"InvalidDateTime", func() (Value, error) { return NewDateTime("2024-02-30 12:00:00") }},
		{"InvalidUUID", func() (Value, error) { return NewUUID("hello") }},
		{"InvalidUUIDHex", func() (Value, error) { return NewUUID("00112233-4455-6677-8899-aabbccddeefg") }},
		{"InvalidDecimal", func() (Value, error) { return NewDecimal("12.3.4") }},
		{"EmptyDecimal", func() (Value, error) { return NewDecimal("") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, err := tc.create()
			require.Error(t, err)
			assert.Equal(t, Value{}, value)
		})
	}
}

func TestValue_BytesAndJSONOwnTheirData(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		input := []byte(`{"a":1}`)
		value := NewBytes(input)
		read := Value.Bytes
		if asJSON {
			var err error
			value, err = NewJSON(input)
			require.NoError(t, err)
			read = Value.JSON
		}
		input[0] = '!'
		got, err := read(value)
		require.NoError(t, err)
		require.Equal(t, []byte(`{"a":1}`), got)
		got[0] = '?'
		again, err := read(value)
		require.NoError(t, err)
		assert.Equal(t, []byte(`{"a":1}`), again)
	}
}

func TestValue_NestedCollectionsProtectOwnership(t *testing.T) {
	input := []byte("original")
	jsonInput := []byte(`{"ok":true}`)
	jsonValue, err := NewJSON(jsonInput)
	require.NoError(t, err)
	sourceMap := map[string]Value{"bytes": NewBytes(input), "json": jsonValue}
	mapValue := NewMap(sourceMap)
	sourceArray := []Value{mapValue}
	value := NewArray(sourceArray)
	input[0], jsonInput[0] = '!', '!'
	sourceMap["bytes"] = NewNull()
	sourceArray[0] = NewNull()

	array, err := value.Array()
	require.NoError(t, err)
	object, err := array[0].Map()
	require.NoError(t, err)
	payload, err := object["bytes"].Bytes()
	require.NoError(t, err)
	assert.Equal(t, []byte("original"), payload)
	payload[0] = '?'
	jsonPayload, err := object["json"].JSON()
	require.NoError(t, err)
	assert.Equal(t, []byte(`{"ok":true}`), jsonPayload)
	jsonPayload[0] = '?'
	delete(object, "bytes")
	object["extra"] = NewBool(true)
	array[0] = NewNull()

	again, err := value.Array()
	require.NoError(t, err)
	objectAgain, err := again[0].Map()
	require.NoError(t, err)
	require.Len(t, objectAgain, 2)
	originalBytes, err := objectAgain["bytes"].Bytes()
	require.NoError(t, err)
	assert.Equal(t, []byte("original"), originalBytes)
	originalJSON, err := objectAgain["json"].JSON()
	require.NoError(t, err)
	assert.Equal(t, []byte(`{"ok":true}`), originalJSON)
}

func TestValue_DecimalAndUUIDAccessorsProtectOwnership(t *testing.T) {
	value, err := NewDecimal("123456789012345678901234567890.125")
	require.NoError(t, err)
	dec, err := value.Decimal()
	require.NoError(t, err)
	dec.Coefficient().SetInt64(0)
	require.NoError(t, dec.UnmarshalText([]byte("999")))
	again, err := value.Decimal()
	require.NoError(t, err)
	assert.Equal(t, "123456789012345678901234567890.125", again.String())
	id, err := NewUUID("00112233-4455-6677-8899-aabbccddeeff")
	require.NoError(t, err)
	parsed, err := id.UUID()
	require.NoError(t, err)
	parsed[0] = 0xff
	original, err := id.UUID()
	require.NoError(t, err)
	assert.Equal(t, "00112233-4455-6677-8899-aabbccddeeff", original.String())
}
