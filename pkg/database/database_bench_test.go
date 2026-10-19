package database

import (
	"bytes"
	"fmt"
	"testing"
)

func BenchmarkDatabase_OpenCloseCached(b *testing.B) {
	for _, count := range []int{0, 1, 128, 255} {
		b.Run(fmt.Sprintf("Entries%d", count), func(b *testing.B) {
			path := databaseBenchmarkFixture(b, count)
			b.ReportAllocs()
			for b.Loop() {
				db, err := Open(path)
				if err != nil {
					b.Fatal(err)
				}
				if err := db.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkDatabase_GetCached(b *testing.B) {
	for _, count := range []int{0, 1, 128, 255} {
		for _, found := range []bool{true, false} {
			if count == 0 && found {
				continue
			}
			b.Run(fmt.Sprintf("Entries%d/Found%t", count, found), func(b *testing.B) {
				db := openBenchmarkDatabase(b, count)
				entries := fixtureEntries(max(count, 1))
				keys := make([][]byte, len(entries))
				for i := range keys {
					number := i * 2
					if !found {
						number++
					}
					keys[i] = fmt.Appendf(keys[i], "%04d", number)
				}
				b.ReportAllocs()
				i := 0
				for b.Loop() {
					value, gotFound, err := db.Get(keys[i])
					if err != nil || gotFound != found {
						b.Fatalf("found=%t err=%v", gotFound, err)
					}
					if found && !bytes.Equal(value, entries[i].value) {
						b.Fatal("unexpected value")
					}
					i = (i + 1) % len(keys)
				}
			})
		}
	}
}

func BenchmarkDatabase_GetCachedThreeLevels(b *testing.B) {
	fixture, path := newTraversalDatabase(b, 7, traversalPages(b))
	if err := fixture.Close(); err != nil {
		b.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := db.Close(); err != nil {
			b.Error(err)
		}
	})
	keys := [][]byte{[]byte("ant"), []byte("cat"), []byte("dog"), []byte("yak")}
	want := [][]byte{[]byte("value:ant"), []byte("value:cat"), []byte("value:dog"), []byte("value:yak")}
	for _, key := range keys {
		if _, _, err := db.Get(key); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		value, found, err := db.Get(keys[i])
		if err != nil || !found || !bytes.Equal(value, want[i]) {
			b.Fatalf("unexpected result: found=%t err=%v", found, err)
		}
		i = (i + 1) % len(keys)
	}
}

func BenchmarkDatabase_PutReplaceSynced(b *testing.B) {
	for _, count := range []int{1, 128, 255} {
		b.Run(fmt.Sprintf("Entries%d", count), func(b *testing.B) {
			db := openBenchmarkDatabase(b, count)
			entries := fixtureEntries(count)
			value := []byte("new!")
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				if err := db.Put(entries[i].key, value); err != nil {
					b.Fatal(err)
				}
				i = (i + 1) % count
			}
		})
	}
}

func BenchmarkDatabase_PutReplaceSyncedThreeLevels(b *testing.B) {
	fixture, path := newTraversalDatabase(b, 7, traversalPages(b))
	if err := fixture.Close(); err != nil {
		b.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := db.Close(); err != nil {
			b.Error(err)
		}
	})
	keys := [][]byte{[]byte("ant"), []byte("cat"), []byte("dog"), []byte("yak")}
	value := []byte("replacement")
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		if err := db.Put(keys[i], value); err != nil {
			b.Fatal(err)
		}
		i = (i + 1) % len(keys)
	}
}

func BenchmarkDatabase_PutInsertSyncedBatch255(b *testing.B) {
	for _, order := range []string{"Ascending", "Descending", "Mixed"} {
		b.Run(order, func(b *testing.B) {
			db := openBenchmarkDatabase(b, 0)
			entries := fixtureEntries(255)
			ordered := make([]testEntry, 255)
			for i := range ordered {
				index := i
				if order == "Descending" {
					index = 254 - i
				}
				if order == "Mixed" {
					index = (i * 73) % 255
				}
				ordered[i] = entries[index]
			}
			emptyRoot := validDatabaseBytes()[4096:]
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				if err := db.pager.WritePage(db.header.RootPageID, emptyRoot); err != nil {
					b.Fatal(err)
				}
				if err := db.pager.Flush(); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				for _, entry := range ordered {
					if err := db.Put(entry.key, entry.value); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

func databaseBenchmarkFixture(b *testing.B, count int) string {
	b.Helper()
	path := writeDatabaseFixture(b, databaseBytesWithEntries(fixtureEntries(count)))
	if err := Validate(path); err != nil {
		b.Fatal(err)
	}
	return path
}

func openBenchmarkDatabase(b *testing.B, count int) *Database {
	b.Helper()
	db, err := Open(databaseBenchmarkFixture(b, count))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := db.Close(); err != nil {
			b.Error(err)
		}
	})
	return db
}
