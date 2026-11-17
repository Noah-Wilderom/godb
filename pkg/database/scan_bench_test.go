package database

import (
	"fmt"
	"testing"
)

func BenchmarkDatabase_Scan(b *testing.B) {
	db, _ := newTraversalDatabase(b, 7, traversalPages(b))
	b.ReportAllocs()

	for b.Loop() {
		if _, err := db.Scan([]byte("cat"), []byte("zebra"), 8); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDatabase_ScanRowsByIndex(b *testing.B) {
	db, _, table := indexScanFixture(b)

	for _, limit := range []int{1, 10, 25} {
		b.Run(fmt.Sprintf("Limit%d", limit), func(b *testing.B) {
			value := scanFixtureRow(1)[2]
			b.ReportAllocs()

			for b.Loop() {
				if _, err := db.ScanRowsByIndex(table, 9, limit, value); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
