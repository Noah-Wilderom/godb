package database

import (
	"testing"

	"github.com/stretchr/testify/require"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func BenchmarkDatabase_UpdateRow(b *testing.B) {
	for _, mode := range []string{"Noop", "Unindexed", "Indexed"} {
		b.Run(mode, func(b *testing.B) {
			db, _ := openWriterDatabase(b)
			table, row := rowInsertFixture()
			table.Columns = append(table.Columns, schema.Column{ID: 100, Name: "extra", Type: recordpkg.TypeBool})
			row[100] = recordpkg.NewBool(false)
			require.NoError(b, db.CreateTable(table))
			require.NoError(b, db.InsertRow(table, row))
			values := []recordpkg.Value{recordpkg.NewBytes([]byte("one")), recordpkg.NewBytes([]byte("two"))}
			iteration := 0
			b.ReportAllocs()

			for b.Loop() {
				iteration++

				switch mode {
				case "Unindexed":
					row[100] = recordpkg.NewBool(iteration%2 == 1)
				case "Indexed":
					row[2] = values[iteration%2]
				}

				if matched, err := db.UpdateRow(table, row); err != nil || !matched {
					b.Fatalf("matched=%t err=%v", matched, err)
				}
			}
		})
	}
}
