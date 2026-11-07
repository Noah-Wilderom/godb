package record

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func BenchmarkDecodeValue(b *testing.B) {
	for _, tc := range valueEncodingCases(b) {
		b.Run(tc.name, func(b *testing.B) {
			encoded, err := hex.DecodeString(tc.hex)
			if err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			b.SetBytes(int64(len(encoded)))

			for b.Loop() {
				value, err := DecodeValue(encoded)
				if err != nil || value.Type() != tc.value.Type() {
					b.Fatal("unexpected decoding result", err)
				}
			}
		})
	}
}

func BenchmarkEncodeValue(b *testing.B) {
	must := func(value Value, err error) Value {
		b.Helper()

		if err != nil {
			b.Fatal(err)
		}

		return value
	}

	cases := []struct {
		name  string
		value Value
	}{
		{"Null", NewNull()},
		{"Bool", NewBool(true)},
		{"Int64", NewInt64(-42)},
		{"Uint64", NewUint64(42)},
		{"Float64", NewFloat64(123.5)},
		{"Float32", NewFloat32(123.5)},
		{"Text", must(NewText("hello world"))},
		{"Bytes4096", NewBytes(make([]byte, 4096))},
		{"JSON", must(NewJSON([]byte(`{"name":"Noah","active":true}`)))},
		{"Date", must(NewDate("2024-02-29"))},
		{"Time", must(NewTime("12:34:56.123456789"))},
		{"DateTime", must(NewDateTime("2024-02-29 12:34:56"))},
		{"UUID", must(NewUUID("00112233-4455-6677-8899-aabbccddeeff"))},
		{"Decimal", must(NewDecimal("12345678901234567890.12345"))},
		{"Timestamp", NewTimestamp(1234567890)},
		{"Duration", NewDuration(90)},
		{"Array", NewArray([]Value{NewInt64(42), NewBool(true), NewBytes(bytes.Repeat([]byte{'a'}, 64))})},
		{"Map", NewMap(map[string]Value{"z": NewInt64(1), "a": NewBool(true), "nested": NewArray([]Value{NewNull(), NewInt64(2)})})},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			expected, err := EncodeValue(tc.value)
			if err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			b.SetBytes(int64(len(expected)))

			for b.Loop() {
				encoded, err := EncodeValue(tc.value)
				if err != nil || len(encoded) != len(expected) {
					b.Fatal("unexpected encoding result", err)
				}
			}
		})
	}
}

func BenchmarkEncodeValueProfiles(b *testing.B) {
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
