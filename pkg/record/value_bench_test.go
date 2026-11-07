package record

import (
	"bytes"
	"fmt"
	"testing"
)

func BenchmarkValue_Bytes(b *testing.B) {
	for _, size := range []int{0, 64, 4096} {
		b.Run(fmt.Sprintf("Bytes%d", size), func(b *testing.B) {
			value := NewBytes(make([]byte, size))
			b.ReportAllocs()
			b.SetBytes(int64(size))

			for b.Loop() {
				got, err := value.Bytes()
				if err != nil || len(got) != size {
					b.Fatal("unexpected bytes result", err)
				}
			}
		})
	}
}

func BenchmarkNewJSON(b *testing.B) {
	for _, size := range []int{64, 4096} {
		b.Run(fmt.Sprintf("TextBytes%d", size), func(b *testing.B) {
			input := append([]byte(`{"text":"`), bytes.Repeat([]byte{'a'}, size)...)
			input = append(input, '"', '}')
			b.ReportAllocs()
			b.SetBytes(int64(len(input)))

			for b.Loop() {
				value, err := NewJSON(input)
				if err != nil || value.Type() != TypeJSON {
					b.Fatal("unexpected JSON result", err)
				}
			}
		})
	}
}
