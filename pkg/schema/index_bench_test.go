package schema

import (
	"fmt"
	"testing"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func BenchmarkValidateIndexes(b *testing.B) {
	for _, count := range []uint32{0, 1, 8, 32} {
		for _, width := range []uint32{1, 4} {
			b.Run(fmt.Sprintf("Indexes%d/ColumnsPerIndex%d", count, width), func(b *testing.B) {
				columns := make([]Column, 16)

				for i := range uint32(len(columns)) {
					columns[i] = Column{ID: i + 1, Name: fmt.Sprintf("column_%d", i), Type: record.TypeUint64}
				}

				indexes := make([]Index, count)

				for i := range count {
					ids := make([]uint32, width)

					for j := range width {
						ids[j] = (i+j)%16 + 1
					}

					indexes[i] = Index{ID: i + 1, Name: fmt.Sprintf("index_%d", i), ColumnIDs: ids, Unique: i%2 == 0}
				}

				b.ReportAllocs()

				for b.Loop() {
					if err := ValidateIndexes(columns, indexes); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
