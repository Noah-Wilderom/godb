package raftstore

import (
	"fmt"
	"math"
	"testing"

	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
)

func BenchmarkStoreSave(b *testing.B) {
	for _, count := range []int{1, 64} {
		b.Run(fmt.Sprintf("Entries%d", count), func(b *testing.B) {
			store, _ := testStore(b)
			if err := store.Save(raft.Ready{HardState: testHard(1, 1, 0)}); err != nil {
				b.Fatal(err)
			}

			index := uint64(1)
			b.ReportAllocs()
			b.SetBytes(int64(count * 256))
			b.ResetTimer()

			for b.Loop() {
				entries := make([]*raftpb.Entry, count)
				for i := range entries {
					entries[i] = testEntry(index, 1, string(make([]byte, 256)))
					index++
				}

				if err := store.Save(raft.Ready{HardState: testHard(1, 1, index-1), Entries: entries}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkStoreEntries(b *testing.B) {
	store, _ := testStore(b)

	entries := make([]*raftpb.Entry, 64)
	for i := range entries {
		entries[i] = testEntry(uint64(i)+1, 1, string(make([]byte, 256)))
	}

	if err := store.Save(raft.Ready{HardState: testHard(1, 1, 64), Entries: entries}); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		if _, err := store.Entries(1, 65, math.MaxUint64); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStoreOpen(b *testing.B) {
	store, path := testStore(b)

	entries := make([]*raftpb.Entry, 1024)
	for i := range entries {
		entries[i] = testEntry(uint64(i)+1, 1, string(make([]byte, 256)))
	}

	if err := store.Save(raft.Ready{HardState: testHard(1, 1, 1024), Entries: entries}); err != nil {
		b.Fatal(err)
	}

	if err := store.Close(); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		opened, err := Open(path, testIdentity())
		if err != nil {
			b.Fatal(err)
		}

		if err := opened.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
