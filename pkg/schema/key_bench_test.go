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

func BenchmarkEncodeUniqueIndexKey(b *testing.B) {
	table, row := indexEntryFixture(b, true)
	table.Indexes[0].ColumnIDs = []uint32{7, 100}
	b.ReportAllocs()

	for b.Loop() {
		if _, err := EncodeUniqueIndexKey(table, 3, row[7], row[100]); err != nil {
			b.Fatal(err)
		}
	}
}
