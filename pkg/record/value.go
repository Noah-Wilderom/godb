package record

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/shopspring/decimal"
)

type Value struct {
	typ  Type
	data any
}

func NewNull() Value {
	return Value{typ: TypeNull, data: nil}
}

func NewInt64(i int64) Value {
	return Value{typ: TypeInt64, data: i}
}

func NewBool(b bool) Value {
	return Value{typ: TypeBool, data: b}
}

func NewText(s string) (Value, error) {
	if valid := utf8.ValidString(s); !valid {
		return Value{}, errors.New("invalid UTF-8 string")
	}

	return Value{typ: TypeText, data: s}, nil
}

func NewBytes(b []byte) Value {
	return Value{typ: TypeBytes, data: bytes.Clone(b)}
}

func NewFloat64(f float64) Value {
	return Value{typ: TypeFloat64, data: f}
}

func NewJSON(j []byte) (Value, error) {
	if valid := json.Valid(j); !valid {
		return Value{}, errors.New("invalid JSON format")
	}

	return Value{typ: TypeJSON, data: bytes.Clone(j)}, nil
}

func NewDate(d string) (Value, error) {
	parsed, err := time.Parse("2006-01-02", d)
	if err != nil {
		return Value{}, errors.New("invalid date format, expected YYYY-MM-DD")
	}

	return Value{typ: TypeDate, data: parsed}, nil
}

func NewTime(t string) (Value, error) {
	parsed, err := time.Parse("15:04:05", t)
	if err != nil {
		return Value{}, errors.New("invalid time format, expected HH:MM:SS")
	}

	return Value{typ: TypeTime, data: parsed}, nil
}

func NewDateTime(dt string) (Value, error) {
	parsed, err := time.Parse("2006-01-02 15:04:05", dt)
	if err != nil {
		return Value{}, errors.New("invalid datetime format, expected YYYY-MM-DD HH:MM:SS")
	}

	return Value{typ: TypeDateTime, data: parsed}, nil
}

func NewUUID(u string) (Value, error) {
	parsed, err := uuid.Parse(u)
	if err != nil {
		return Value{}, errors.New("invalid UUID format")
	}

	return Value{typ: TypeUUID, data: parsed}, nil
}

func NewDecimal(d string) (Value, error) {
	dec, err := decimal.NewFromString(d)
	if err != nil {
		return Value{}, errors.New("invalid decimal format")
	}

	return Value{typ: TypeDecimal, data: dec}, nil
}

func NewMap(m map[string]Value) Value {
	return Value{typ: TypeMap, data: maps.Clone(m)}
}

func NewTimestamp(ts int64) Value {
	return Value{typ: TypeTimestamp, data: ts}
}

func NewUint64(u uint64) Value {
	return Value{typ: TypeUint64, data: u}
}

func NewFloat32(f float32) Value {
	return Value{typ: TypeFloat32, data: f}
}

func NewDuration(d int64) Value {
	return Value{typ: TypeDuration, data: d}
}

func NewArray(a []Value) Value {
	return Value{typ: TypeArray, data: slices.Clone(a)}
}

func (v Value) Type() Type {
	return v.typ
}

func (v Value) Int64() (int64, error) {
	if v.typ != TypeInt64 {
		return 0, errors.New("value is not of type Int64")
	}

	value, ok := v.data.(int64)
	if !ok {
		return 0, errors.New("value is not of type Int64")
	}

	return value, nil
}

func (v Value) Uint64() (uint64, error) {
	if v.typ != TypeUint64 {
		return 0, errors.New("value is not of type Uint64")
	}

	value, ok := v.data.(uint64)
	if !ok {
		return 0, errors.New("value is not of type Uint64")
	}

	return value, nil
}

func (v Value) Bool() (bool, error) {
	if v.typ != TypeBool {
		return false, errors.New("value is not of type Bool")
	}

	value, ok := v.data.(bool)
	if !ok {
		return false, errors.New("value is not of type Bool")
	}

	return value, nil
}

func (v Value) Text() (string, error) {
	if v.typ != TypeText {
		return "", errors.New("value is not of type Text")
	}

	value, ok := v.data.(string)
	if !ok {
		return "", errors.New("value is not of type Text")
	}

	return value, nil
}

func (v Value) Bytes() ([]byte, error) {
	if v.typ != TypeBytes {
		return nil, errors.New("value is not of type Bytes")
	}

	value, ok := v.data.([]byte)
	if !ok {
		return nil, errors.New("value is not of type Bytes")
	}

	return bytes.Clone(value), nil
}

func (v Value) Float64() (float64, error) {
	if v.typ != TypeFloat64 {
		return 0, errors.New("value is not of type Float64")
	}

	value, ok := v.data.(float64)
	if !ok {
		return 0, errors.New("value is not of type Float64")
	}

	return value, nil
}

func (v Value) JSON() ([]byte, error) {
	if v.typ != TypeJSON {
		return nil, errors.New("value is not of type JSON")
	}

	value, ok := v.data.([]byte)
	if !ok {
		return nil, errors.New("value is not of type JSON")
	}

	return bytes.Clone(value), nil
}

func (v Value) Date() (time.Time, error) {
	if v.typ != TypeDate {
		return time.Time{}, errors.New("value is not of type Date")
	}

	value, ok := v.data.(time.Time)
	if !ok {
		return time.Time{}, errors.New("value is not of type Date")
	}

	return value, nil
}

func (v Value) Time() (time.Time, error) {
	if v.typ != TypeTime {
		return time.Time{}, errors.New("value is not of type Time")
	}

	value, ok := v.data.(time.Time)
	if !ok {
		return time.Time{}, errors.New("value is not of type Time")
	}

	return value, nil
}

func (v Value) DateTime() (time.Time, error) {
	if v.typ != TypeDateTime {
		return time.Time{}, errors.New("value is not of type DateTime")
	}

	value, ok := v.data.(time.Time)
	if !ok {
		return time.Time{}, errors.New("value is not of type DateTime")
	}

	return value, nil
}

func (v Value) UUID() (uuid.UUID, error) {
	if v.typ != TypeUUID {
		return uuid.UUID{}, errors.New("value is not of type UUID")
	}

	value, ok := v.data.(uuid.UUID)
	if !ok {
		return uuid.UUID{}, errors.New("value is not of type UUID")
	}

	return value, nil
}

func (v Value) Decimal() (decimal.Decimal, error) {
	if v.typ != TypeDecimal {
		return decimal.Decimal{}, errors.New("value is not of type Decimal")
	}

	value, ok := v.data.(decimal.Decimal)
	if !ok {
		return decimal.Decimal{}, errors.New("value is not of type Decimal")
	}

	return value.Copy(), nil
}

func (v Value) Map() (map[string]Value, error) {
	if v.typ != TypeMap {
		return nil, errors.New("value is not of type Map")
	}

	value, ok := v.data.(map[string]Value)
	if !ok {
		return nil, errors.New("value is not of type Map")
	}

	return maps.Clone(value), nil
}

func (v Value) Timestamp() (int64, error) {
	if v.typ != TypeTimestamp {
		return 0, errors.New("value is not of type Timestamp")
	}

	value, ok := v.data.(int64)
	if !ok {
		return 0, errors.New("value is not of type Timestamp")
	}

	return value, nil
}

func (v Value) Float32() (float32, error) {
	if v.typ != TypeFloat32 {
		return 0, errors.New("value is not of type Float32")
	}

	value, ok := v.data.(float32)
	if !ok {
		return 0, errors.New("value is not of type Float32")
	}

	return value, nil
}

func (v Value) Duration() (int64, error) {
	if v.typ != TypeDuration {
		return 0, errors.New("value is not of type Duration")
	}

	value, ok := v.data.(int64)
	if !ok {
		return 0, errors.New("value is not of type Duration")
	}

	return value, nil
}

func (v Value) Array() ([]Value, error) {
	if v.typ != TypeArray {
		return nil, errors.New("value is not of type Array")
	}

	value, ok := v.data.([]Value)
	if !ok {
		return nil, errors.New("value is not of type Array")
	}

	return slices.Clone(value), nil
}

func (v Value) IsNull() bool {
	return v.typ == TypeNull
}
