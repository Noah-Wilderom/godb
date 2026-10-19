package database

import (
	"fmt"
	"os"
	"testing"
)

func BenchmarkDatabase_PutNonRootSplitSynced(b *testing.B) {
	for _, valueSize := range []int{1200, 2993} {
		b.Run(fmt.Sprintf("InsertedValueBytes%d", valueSize), func(b *testing.B) {
			pages := traversalPages(b)
			pages[0] = databaseBytesWithEntries([]testEntry{rootSplitEntry("ant", 1993), rootSplitEntry("bee", 1993)})[4096:]
			db, path := newTraversalDatabase(b, 7, pages)
			initial, err := os.ReadFile(path)
			if err != nil {
				b.Fatal(err)
			}
			header := *db.header
			entry := rootSplitEntry("bat", valueSize)
			wantCount := uint64(9)
			if valueSize == 2993 {
				wantCount = 10
			}
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				if err := os.WriteFile(path, initial, 0600); err != nil {
					b.Fatal(err)
				}
				*db.header = header
				b.StartTimer()
				if err := db.Put(entry.key, entry.value); err != nil {
					b.Fatal(err)
				}
				if db.header.RootPageID != 7 || db.header.PageCount != wantCount {
					b.Fatal("unexpected non-root split metadata")
				}
			}
		})
	}
}

func BenchmarkDatabase_PutRootSplitSynced(b *testing.B) {
	cases := []struct {
		name      string
		initial   []testEntry
		update    testEntry
		pageCount uint64
	}{
		{"TwoLeaves", []testEntry{rootSplitEntry("a", 2000)}, rootSplitEntry("b", 2084), 4},
		{"ThreeLeaves", []testEntry{rootSplitEntry("a", 1995), rootSplitEntry("c", 1995)}, rootSplitEntry("b", 2995), 5},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			initial := databaseBytesWithEntries(tc.initial)
			path := writeDatabaseFixture(b, initial)
			db, err := Open(path)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				if err := db.Close(); err != nil {
					b.Error(err)
				}
			})
			header := *db.header
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				if err := os.WriteFile(path, initial, 0600); err != nil {
					b.Fatal(err)
				}
				*db.header = header
				b.StartTimer()
				if err := db.Put(tc.update.key, tc.update.value); err != nil {
					b.Fatal(err)
				}
				if db.header.PageCount != tc.pageCount || db.header.RootPageID != tc.pageCount-1 {
					b.Fatal("unexpected root split metadata")
				}
			}
		})
	}
}

func BenchmarkPrepareLeafSplit(b *testing.B) {
	cases := []struct {
		name  string
		sizes []int
		pages int
	}{
		{"Empty", nil, 1},
		{"AlreadyFits", []int{100, 200, 300}, 1},
		{"ExactFit", []int{4088}, 1},
		{"TwoPages", []int{2000, 2084}, 2},
		{"ThreePages", []int{1995, 2995, 1995}, 3},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			leaf, _ := splitPlanFixture(b, tc.sizes)
			b.ReportAllocs()
			for b.Loop() {
				plan, err := prepareLeafSplit(leaf)
				if err != nil {
					b.Fatal(err)
				}
				if len(plan.pages) != tc.pages || len(plan.separators) != tc.pages-1 {
					b.Fatal("unexpected split plan size")
				}
			}
		})
	}
}
