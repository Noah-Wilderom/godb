package database

import (
	"fmt"
	"math"
	"testing"
)

func BenchmarkDatabase_CreateTableSynced(b *testing.B) {
	db, _ := openWriterDatabase(b)
	table, _ := rowInsertFixture()
	table.ID = 0
	b.ReportAllocs()

	for b.Loop() {
		if table.ID == math.MaxUint32 {
			b.Fatal("exhausted table IDs")
		}

		table.ID++

		table.Name = fmt.Sprintf("table_%d", table.ID)
		if err := db.CreateTable(table); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDatabase_GetTable(b *testing.B) {
	for _, byName := range []bool{false, true} {
		b.Run(fmt.Sprintf("ByName%t", byName), func(b *testing.B) {
			db, _ := openWriterDatabase(b)

			table, _ := rowInsertFixture()
			if err := db.CreateTable(table); err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()

			for b.Loop() {
				var (
					found bool
					err   error
				)

				if byName {
					_, found, err = db.GetTableByName(table.Name)
				} else {
					_, found, err = db.GetTable(table.ID)
				}

				if err != nil || !found {
					b.Fatalf("found=%t err=%v", found, err)
				}
			}
		})
	}
}
