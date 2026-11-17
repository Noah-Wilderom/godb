package database

import (
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func BenchmarkWriteTxReads(b *testing.B) {
	for _, mode := range []string{"ReadOnly", "ReadModifyWrite", "ReadOwnWrites", "Scan"} {
		b.Run(mode, func(b *testing.B) {
			db, _ := openWriterDatabase(b)
			key := []byte("counter")
			require.NoError(b, db.Put(key, make([]byte, 8)))
			b.ReportAllocs()

			for b.Loop() {
				tx := db.BeginWrite()

				value, found, err := tx.Get(key)
				if err != nil || !found {
					b.Fatalf("found=%t err=%v", found, err)
				}

				if mode == "ReadModifyWrite" || mode == "ReadOwnWrites" {
					binary.LittleEndian.PutUint64(value, binary.LittleEndian.Uint64(value)+1)

					if err := tx.Put(key, value); err != nil {
						b.Fatal(err)
					}
				}

				if mode == "ReadOwnWrites" {
					if _, _, err := tx.Get(key); err != nil {
						b.Fatal(err)
					}
				}

				if mode == "Scan" {
					if _, err := tx.Scan(nil, nil, 10); err != nil {
						b.Fatal(err)
					}
				}

				if err := tx.Commit(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkWriteTx(b *testing.B) {
	for _, count := range []int{1, 16, MaxBatchEntries} {
		b.Run(fmt.Sprintf("Keys%d", count), func(b *testing.B) {
			db, _ := openWriterDatabase(b)

			entries := make([]Entry, count)
			for i := range entries {
				entries[i] = Entry{Key: fmt.Appendf(nil, "key%03d", i), Value: []byte("value")}
			}

			b.ReportAllocs()

			for b.Loop() {
				tx := db.BeginWrite()
				for _, entry := range entries {
					if err := tx.Put(entry.Key, entry.Value); err != nil {
						b.Fatal(err)
					}
				}

				if err := tx.Commit(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkWriteTxIndexedRows(b *testing.B) {
	db, _ := openWriterDatabase(b)
	table, _ := rowInsertFixture()
	require.NoError(b, db.CreateTable(table))
	first, second := numberedRow(1), numberedRow(2)
	b.ReportAllocs()

	for b.Loop() {
		tx := db.BeginWrite()
		if err := tx.InsertRow(table, first); err != nil {
			b.Fatal(err)
		}

		if err := tx.InsertRow(table, second); err != nil {
			b.Fatal(err)
		}

		if err := tx.DeleteRow(table, first[1]); err != nil {
			b.Fatal(err)
		}

		if err := tx.DeleteRow(table, second[1]); err != nil {
			b.Fatal(err)
		}

		if err := tx.Commit(); err != nil {
			b.Fatal(err)
		}
	}
}
