package schema

import (
	"testing"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func BenchmarkRowCodec(b *testing.B) {
	wideTable, wideRow := rowCodecFixture(b)

	cases := []struct {
		name  string
		table Table
		row   Row
	}{
		{"Small", testTable(), Row{7: record.NewNull(), 42: record.NewUint64(42)}},
		{"AllTypes", wideTable, wideRow},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			encoded, err := EncodeRow(tc.table, tc.row)
			if err != nil {
				b.Fatal(err)
			}

			b.Run("Encode", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(encoded)))

				for b.Loop() {
					if _, err := EncodeRow(tc.table, tc.row); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("Decode", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(encoded)))

				for b.Loop() {
					if _, err := DecodeRow(tc.table, encoded); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
