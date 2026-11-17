package database

import "testing"

func BenchmarkDatabase_ScanPage(b *testing.B) {
	db, _ := newTraversalDatabase(b, 7, traversalPages(b))

	first, err := db.ScanPage(nil, nil, 2, nil)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()

	for b.Loop() {
		if _, err := db.ScanPage(nil, nil, 2, first.Next); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDatabase_ScanRowsByIndexPage(b *testing.B) {
	db, _, table := indexScanFixture(b)

	first, err := db.ScanRowsByIndexPage(table, 9, 10, nil)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()

	for b.Loop() {
		if _, err := db.ScanRowsByIndexPage(table, 9, 10, first.Next); err != nil {
			b.Fatal(err)
		}
	}
}
