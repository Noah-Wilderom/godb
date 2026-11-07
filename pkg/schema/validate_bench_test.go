package schema

import (
	"fmt"
	"testing"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func BenchmarkValidateColumns(b *testing.B) {
	for _, count := range []uint32{1, 16, 64, 256} {
		b.Run(fmt.Sprintf("Columns%d", count), func(b *testing.B) {
			columns := make([]Column, count)

			for i := range count {
				columns[i] = Column{ID: i + 1, Name: fmt.Sprintf("column_%d", i), Type: record.TypeText}
			}

			b.ReportAllocs()

			for b.Loop() {
				if err := ValidateColumns(columns); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkValidateTable(b *testing.B) {
	for _, count := range []uint32{1, 16, 64, 256} {
		b.Run(fmt.Sprintf("Columns%d", count), func(b *testing.B) {
			columns := make([]Column, count)

			for i := range count {
				columns[i] = Column{ID: i + 1, Name: fmt.Sprintf("column_%d", i), Type: record.TypeText}
			}

			table := Table{ID: 1, Name: "users", Columns: columns, PrimaryKeyColumnID: count}
			b.ReportAllocs()

			for b.Loop() {
				if err := ValidateTable(table); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkValidateRow(b *testing.B) {
	for _, count := range []uint32{1, 16, 64, 256} {
		b.Run(fmt.Sprintf("Columns%d", count), func(b *testing.B) {
			columns := make([]Column, count)
			row := make(Row, count)

			for i := range count {
				columns[i] = Column{ID: i + 1, Name: fmt.Sprintf("column_%d", i), Type: record.TypeUint64}
				row[i+1] = record.NewUint64(uint64(i))
			}

			table := Table{ID: 1, Name: "users", Columns: columns, PrimaryKeyColumnID: count}
			b.ReportAllocs()

			for b.Loop() {
				if err := ValidateRow(table, row); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
