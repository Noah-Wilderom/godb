package record

import (
	"bytes"
	"cmp"
	"encoding/hex"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func keyEncodingCases(t testing.TB) []struct {
	name  string
	value Value
	hex   string
} {
	t.Helper()
	text, err := NewText("a\x00b")
	require.NoError(t, err)
	empty, err := NewText("")
	require.NoError(t, err)
	id, err := NewUUID("00112233-4455-6677-8899-aabbccddeeff")
	require.NoError(t, err)
	return []struct {
		name  string
		value Value
		hex   string
	}{
		{"IntMin", NewInt64(math.MinInt64), "010000000000000000"},
		{"IntNegative", NewInt64(-1), "017fffffffffffffff"},
		{"IntZero", NewInt64(0), "018000000000000000"},
		{"IntPositive", NewInt64(256), "018000000000000100"},
		{"IntMax", NewInt64(math.MaxInt64), "01ffffffffffffffff"},
		{"UintZero", NewUint64(0), "0e0000000000000000"},
		{"UintPositive", NewUint64(256), "0e0000000000000100"},
		{"UintMax", NewUint64(math.MaxUint64), "0effffffffffffffff"},
		{"Text", text, "036100ff620000"},
		{"EmptyText", empty, "030000"},
		{"Bytes", NewBytes([]byte{0, 0xff, 0}), "0400ffff00ff0000"},
		{"NilBytes", NewBytes(nil), "040000"},
		{"EmptyBytes", NewBytes([]byte{}), "040000"},
		{"UUID", id, "0a00112233445566778899aabbccddeeff"},
	}
}

func TestEncodeKeyValue_Golden(t *testing.T) {
	for _, tc := range keyEncodingCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			want, err := hex.DecodeString(tc.hex)
			require.NoError(t, err)
			encoded, err := EncodeKeyValue(tc.value)
			require.NoError(t, err)
			require.Equal(t, want, encoded)
		})
	}
}

func TestEncodeKeyValue_Ordering(t *testing.T) {
	groups := make(map[string][]Value)

	for _, number := range []int64{math.MinInt64, -256, -1, 0, 1, 255, 256, math.MaxInt64} {
		groups["Int64"] = append(groups["Int64"], NewInt64(number))
	}

	for _, number := range []uint64{0, 1, 255, 256, 65535, 65536, 1 << 63, math.MaxUint64} {
		groups["Uint64"] = append(groups["Uint64"], NewUint64(number))
	}

	for _, text := range []string{"", "\x00", "\x00\x00", "\x00a", "A", "a", "a\x00", "aa", "é", "🌳"} {
		value, err := NewText(text)
		require.NoError(t, err)
		groups["Text"] = append(groups["Text"], value)
		groups["Bytes"] = append(groups["Bytes"], NewBytes([]byte(text)))
	}

	for _, text := range []string{
		"00000000-0000-0000-0000-000000000000", "00000000-0000-0000-0000-000000000001",
		"00000000-0000-0000-0000-000000000100", "01000000-0000-0000-0000-000000000000",
		"ffffffff-ffff-ffff-ffff-ffffffffffff",
	} {
		value, err := NewUUID(text)
		require.NoError(t, err)
		groups["UUID"] = append(groups["UUID"], value)
	}

	for name, values := range groups {
		t.Run(name, func(t *testing.T) {
			var previous []byte

			for _, value := range values {
				encoded, err := EncodeKeyValue(value)
				require.NoError(t, err)

				if previous != nil {
					require.Negative(t, bytes.Compare(previous, encoded))
					require.False(t, bytes.HasPrefix(encoded, previous))
				}

				previous = encoded
			}
		})
	}
}

func TestEncodeKeyValue_CompositeBoundaries(t *testing.T) {
	tuples := [][2]string{{"", "z"}, {"\x00", ""}, {"a", ""}, {"a", "bc"}, {"a\x00", ""}, {"ab", "c"}}

	var previous []byte

	for _, tuple := range tuples {
		var encoded []byte

		for _, text := range tuple {
			value, err := NewText(text)
			require.NoError(t, err)
			component, err := EncodeKeyValue(value)
			require.NoError(t, err)
			encoded = append(encoded, component...)
		}

		if previous != nil {
			require.Negative(t, bytes.Compare(previous, encoded))
		}

		previous = encoded
	}
}

func TestEncodeKeyValue_Invalid(t *testing.T) {
	for _, typ := range []Type{TypeNull, TypeBool, TypeFloat32, TypeFloat64, TypeJSON, TypeDecimal, TypeDate, TypeTime, TypeDateTime, TypeTimestamp, TypeDuration, TypeArray, TypeMap, Type(0xff)} {
		encoded, err := EncodeKeyValue(Value{typ: typ})
		require.ErrorContains(t, err, "unsupported key value type")
		require.Nil(t, encoded)
	}

	for _, typ := range []Type{TypeInt64, TypeUint64, TypeText, TypeBytes, TypeUUID} {
		encoded, err := EncodeKeyValue(Value{typ: typ, data: false})
		require.Error(t, err)
		require.Nil(t, encoded)
	}

	encoded, err := EncodeKeyValue(Value{typ: TypeText, data: "bad\xff"})
	require.ErrorContains(t, err, "invalid UTF-8")
	require.Nil(t, encoded)
}

func TestEncodeKeyValue_Ownership(t *testing.T) {
	value := NewBytes([]byte{1, 0, 2})
	encoded, err := EncodeKeyValue(value)
	require.NoError(t, err)
	want := bytes.Clone(encoded)
	clear(encoded)
	again, err := EncodeKeyValue(value)
	require.NoError(t, err)
	require.Equal(t, want, again)
	data, err := value.Bytes()
	require.NoError(t, err)
	require.Equal(t, []byte{1, 0, 2}, data)
}

func FuzzEncodeKeyValue_Order(f *testing.F) {
	f.Add([]byte{}, []byte{0}, int64(math.MinInt64), int64(math.MaxInt64))
	f.Add([]byte{0, 0xff}, []byte{0xff}, int64(-1), int64(0))
	f.Add([]byte("a"), []byte("aa"), int64(255), int64(256))
	f.Fuzz(func(t *testing.T, a, b []byte, x, y int64) {
		ka, err := EncodeKeyValue(NewBytes(a))
		require.NoError(t, err)
		kb, err := EncodeKeyValue(NewBytes(b))
		require.NoError(t, err)
		require.Equal(t, bytes.Compare(a, b), bytes.Compare(ka, kb))
		kx, err := EncodeKeyValue(NewInt64(x))
		require.NoError(t, err)
		ky, err := EncodeKeyValue(NewInt64(y))
		require.NoError(t, err)
		require.Equal(t, cmp.Compare(x, y), bytes.Compare(kx, ky))
	})
}
