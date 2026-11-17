package database

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
)

func BenchmarkDatabase_InsertRowSynced(b *testing.B) {
	for _, indexes := range []int{0, 3} {
		b.Run(fmt.Sprintf("Indexes%d", indexes), func(b *testing.B) {
			db, _ := openWriterDatabase(b)
			table, _ := rowInsertFixture()
			table.Indexes = table.Indexes[:indexes]
			require.NoError(b, db.CreateTable(table))
			id := uint64(0)
			b.ReportAllocs()

			for b.Loop() {
				id++
				if err := db.InsertRow(table, numberedRow(id)); err != nil {
					b.Fatal(err)
				}
			}

			b.ReportMetric(float64(db.Stats().Syncs-1)/float64(b.N), "syncs/row")
		})
	}
}

func BenchmarkDatabase_InsertRowConflict(b *testing.B) {
	db, _ := openWriterDatabase(b)

	table, row := rowInsertFixture()
	require.NoError(b, db.CreateTable(table))

	if err := db.InsertRow(table, row); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()

	for b.Loop() {
		if err := db.InsertRow(table, row); !errors.Is(err, ErrDuplicatePrimaryKey) {
			b.Fatal(err)
		}
	}
}

func BenchmarkDatabase_GetRowCached(b *testing.B) {
	db, _ := openWriterDatabase(b)
	table, _ := rowInsertFixture()
	require.NoError(b, db.CreateTable(table))

	for id := uint64(1); id <= 128; id++ {
		if err := db.InsertRow(table, numberedRow(id)); err != nil {
			b.Fatal(err)
		}
	}

	for _, found := range []bool{true, false} {
		b.Run(fmt.Sprintf("Found%t", found), func(b *testing.B) {
			key := recordpkg.NewUint64(64)
			if !found {
				key = recordpkg.NewUint64(999)
			}

			b.ReportAllocs()

			for b.Loop() {
				if _, got, err := db.GetRow(table, key); err != nil || got != found {
					b.Fatalf("found=%t err=%v", got, err)
				}
			}
		})
	}
}

func BenchmarkDatabase_GetRowByUniqueIndexCached(b *testing.B) {
	db, _ := openWriterDatabase(b)
	table, _ := rowInsertFixture()
	require.NoError(b, db.CreateTable(table))

	for id := uint64(1); id <= 128; id++ {
		if err := db.InsertRow(table, numberedRow(id)); err != nil {
			b.Fatal(err)
		}
	}

	for _, found := range []bool{true, false} {
		b.Run(fmt.Sprintf("Found%t", found), func(b *testing.B) {
			value := numberedRow(64)[2]
			if !found {
				value = recordpkg.NewBytes([]byte("missing"))
			}

			b.ReportAllocs()

			for b.Loop() {
				if _, got, err := db.GetRowByUniqueIndex(table, 7, value); err != nil || got != found {
					b.Fatalf("found=%t err=%v", got, err)
				}
			}
		})
	}
}
