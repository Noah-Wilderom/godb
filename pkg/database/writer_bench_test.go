package database

import (
	"fmt"
	"testing"
)

func BenchmarkDatabase_PutBatchSynced(b *testing.B) {
	for _, size := range []int{1, 8, MaxBatchEntries} {
		b.Run(fmt.Sprintf("Entries%d", size), func(b *testing.B) {
			db := openBenchmarkDatabase(b, 128)
			fixture := fixtureEntries(size)

			entries := make([]Entry, size)
			for i, entry := range fixture {
				entries[i] = Entry{Key: entry.key, Value: entry.value}
			}

			b.ReportAllocs()

			for b.Loop() {
				if err := db.PutBatch(entries); err != nil {
					b.Fatal(err)
				}
			}

			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*size), "ns/entry")
			b.ReportMetric(float64(db.Stats().Syncs)/float64(b.N*size), "syncs/entry")
		})
	}
}

func BenchmarkDatabase_PutParallelSynced(b *testing.B) {
	db := openBenchmarkDatabase(b, 128)
	key := []byte("0000")
	value := []byte("new!")
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := db.Put(key, value); err != nil {
				b.Error(err)
				return
			}
		}
	})
	b.ReportMetric(float64(db.Stats().Syncs)/float64(b.N), "syncs/op")
}

func BenchmarkDatabase_GetParallelCached(b *testing.B) {
	db := openBenchmarkDatabase(b, 128)
	key := []byte("0000")
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, found, err := db.Get(key); err != nil || !found {
				b.Errorf("found=%t err=%v", found, err)
				return
			}
		}
	})
}

func BenchmarkDatabase_MixedReadsAndWrites(b *testing.B) {
	db := openBenchmarkDatabase(b, 128)
	key := []byte("0000")
	value := []byte("new!")
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			i++
			if i%10 == 0 {
				if err := db.Put(key, value); err != nil {
					b.Error(err)
					return
				}
			} else if _, found, err := db.Get(key); err != nil || !found {
				b.Errorf("found=%t err=%v", found, err)
				return
			}
		}
	})
	b.ReportMetric(float64(db.Stats().Syncs)/float64(b.N), "syncs/op")
}
