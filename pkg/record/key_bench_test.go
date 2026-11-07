package record

import "testing"

func BenchmarkEncodeKeyValue(b *testing.B) {
	for _, tc := range keyEncodingCases(b) {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				if _, err := EncodeKeyValue(tc.value); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
