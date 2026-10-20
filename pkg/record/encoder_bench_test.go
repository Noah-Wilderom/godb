package record

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func BenchmarkEncodeValue(b *testing.B) {
	mustValue := func(value Value, err error) Value {
		b.Helper()
		if err != nil {
			b.Fatal(err)
		}
		return value
	}
	name := mustValue(NewText("Ada Lovelace"))
	created := mustValue(NewDateTime("2026-10-20 14:30:00"))
	profile := NewMap(map[string]Value{
		"id":      NewInt64(42),
		"name":    name,
		"active":  NewBool(true),
		"created": created,
		"tags":    NewArray([]Value{mustValue(NewText("reader")), mustValue(NewText("author"))}),
	})
	cases := []struct {
		name  string
		value Value
	}{
		{"Null", NewNull()},
		{"Bool", NewBool(true)},
		{"Int64", NewInt64(-1234567890)},
		{"Uint64", NewUint64(1234567890)},
		{"Float32", NewFloat32(123.456)},
		{"Float64", NewFloat64(123.456)},
		{"Text64", mustValue(NewText(strings.Repeat("a", 64)))},
		{"Text4096", mustValue(NewText(strings.Repeat("a", 4096)))},
		{"Bytes64", NewBytes(bytes.Repeat([]byte{0xab}, 64))},
		{"Bytes4096", NewBytes(bytes.Repeat([]byte{0xab}, 4096))},
		{"JSON", mustValue(NewJSON([]byte(`{"active":true,"name":"Ada Lovelace","id":42}`)))},
		{"Date", mustValue(NewDate("2026-10-20"))},
		{"Time", mustValue(NewTime("14:30:00"))},
		{"DateTime", created},
		{"Timestamp", NewTimestamp(1792499400)},
		{"Duration", NewDuration(1500000000)},
		{"UUID", mustValue(NewUUID("550e8400-e29b-41d4-a716-446655440000"))},
		{"Decimal", mustValue(NewDecimal("12345678901234567890.123456789"))},
		{"Array", NewArray([]Value{NewInt64(42), name, NewBool(true), created})},
		{"Map", profile},
		{"NestedArray", NewArray([]Value{profile, profile, profile})},
	}
	for _, fields := range []int{16, 64} {
		values := make(map[string]Value, fields)
		for index := range fields {
			values[fmt.Sprintf("field_%02d", index)] = NewInt64(int64(index))
		}
		cases = append(cases, struct {
			name  string
			value Value
		}{fmt.Sprintf("Map%d", fields), NewMap(values)})
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				encoded, err := EncodeValue(tc.value)
				if err != nil || len(encoded) == 0 {
					b.Fatal("unexpected encoded value", err)
				}
			}
		})
	}
}
