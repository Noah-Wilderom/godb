package database

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func BenchmarkDatabase_PutDeleteBatchCycle(b *testing.B) {
	for _, size := range []int{1, 16, MaxBatchEntries} {
		b.Run(fmt.Sprintf("Entries%d", size), func(b *testing.B) {
			db, _ := openWriterDatabase(b)
			entries := make([]Entry, size)
			keys := make([][]byte, size)

			for i := range entries {
				keys[i] = fmt.Appendf(nil, "key_%04d", i)
				entries[i] = Entry{Key: keys[i], Value: []byte("value")}
			}

			b.ReportAllocs()

			for b.Loop() {
				if err := db.PutBatch(entries); err != nil {
					b.Fatal(err)
				}

				if deleted, err := db.DeleteBatch(keys); err != nil || deleted != size {
					b.Fatalf("deleted=%d err=%v", deleted, err)
				}
			}
		})
	}
}

func BenchmarkDatabase_InsertDeleteRowCycle(b *testing.B) {
	for _, indexes := range []int{0, 3} {
		b.Run(fmt.Sprintf("Indexes%d", indexes), func(b *testing.B) {
			db, _ := openWriterDatabase(b)
			table, row := rowInsertFixture()
			table.Indexes = table.Indexes[:indexes]
			require.NoError(b, db.CreateTable(table))
			b.ReportAllocs()

			for b.Loop() {
				if err := db.InsertRow(table, row); err != nil {
					b.Fatal(err)
				}

				if deleted, err := db.DeleteRow(table, row[1]); err != nil || !deleted {
					b.Fatalf("deleted=%t err=%v", deleted, err)
				}
			}
		})
	}
}

func BenchmarkDatabase_DeleteMissing(b *testing.B) {
	db, _ := openWriterDatabase(b)
	key := []byte("missing")
	b.ReportAllocs()

	for b.Loop() {
		if deleted, err := db.Delete(key); err != nil || deleted {
			b.Fatalf("deleted=%t err=%v", deleted, err)
		}
	}
}
