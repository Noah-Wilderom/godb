package schema

import (
	"testing"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func BenchmarkEncodePrimaryKey(b *testing.B) {
	table := testTable()
	value := record.NewUint64(42)
	b.ReportAllocs()

	for b.Loop() {
		if _, err := EncodePrimaryKey(table, value); err != nil {
			b.Fatal(err)
		}
	}
}
