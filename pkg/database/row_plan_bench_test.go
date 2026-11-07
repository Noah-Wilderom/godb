package database

import (
	"fmt"
	"testing"

	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func BenchmarkPlanRowInsert(b *testing.B) {
	for _, count := range []uint32{0, 1, 8, MaxBatchEntries - 1} {
		b.Run(fmt.Sprintf("Indexes%d", count), func(b *testing.B) {
			table, row := rowInsertFixture()
			table.Indexes = nil

			for i := range count {
				table.Indexes = append(table.Indexes, schema.Index{ID: i + 1, Name: fmt.Sprintf("index_%d", i), ColumnIDs: []uint32{2, 3}, Unique: i%2 == 0})
			}

			b.ReportAllocs()

			for b.Loop() {
				if _, err := PlanRowInsert(table, row); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
