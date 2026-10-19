package database

import (
	"fmt"
	"testing"
)

func BenchmarkValidateCached(b *testing.B) {
	for _, count := range []int{0, 1, 128, 255} {
		b.Run(fmt.Sprintf("Entries%d", count), func(b *testing.B) {
			path := databaseBenchmarkFixture(b, count)
			b.ReportAllocs()

			for b.Loop() {
				if err := Validate(path); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
