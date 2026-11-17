package database

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
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

func BenchmarkValidateCatalog(b *testing.B) {
	for _, count := range []int{0, 100} {
		b.Run(fmt.Sprintf("Rows%d", count), func(b *testing.B) {
			db, path := openWriterDatabase(b)
			table, _ := rowInsertFixture()
			require.NoError(b, db.CreateTable(table))

			for id := range count {
				require.NoError(b, db.InsertRow(table, numberedRow(uint64(id+1))))
			}

			require.NoError(b, db.Close())
			b.ReportAllocs()

			for b.Loop() {
				if err := Validate(path); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
