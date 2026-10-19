package pager

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkPager_ReadPageCached(b *testing.B) {
	const pageCount = 256
	pager := newPagerForBenchmark(b, pageCount)
	// Warm the file cache before measurement; this is not a cold-storage benchmark.
	for pageID := range uint64(pageCount) {
		if _, err := pager.ReadPage(pageID); err != nil {
			b.Fatal(err)
		}
	}
	b.SetBytes(int64(pagerByteSize))
	b.ReportAllocs()

	var pageID uint64
	for b.Loop() {
		if _, err := pager.ReadPage(pageID); err != nil {
			b.Fatal(err)
		}
		pageID = (pageID + 1) % pageCount
	}
}

func BenchmarkPager_WritePageBuffered(b *testing.B) {
	const pageCount = 256
	pager := newPagerForBenchmark(b, pageCount)
	page := bytes.Repeat([]byte{0x42}, pagerByteSize)
	b.SetBytes(int64(pagerByteSize))
	b.ReportAllocs()

	// Overwrite existing pages so the file stays at 1 MiB regardless of b.N.
	// No Sync is performed: this measures buffered writes, not durable commits.
	var pageID uint64
	for b.Loop() {
		if err := pager.WritePage(pageID, page); err != nil {
			b.Fatal(err)
		}
		pageID = (pageID + 1) % pageCount
	}
}

func BenchmarkPager_AppendPageBufferedBatch256(b *testing.B) {
	const pageCount = 256
	p := newPagerForBenchmark(b, 0)
	page := bytes.Repeat([]byte{0x42}, pagerByteSize)
	b.SetBytes(int64(pageCount * pagerByteSize))
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		if err := p.file.Truncate(0); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		for i := range pageCount {
			id, err := p.AppendPage(page)
			if err != nil {
				b.Fatal(err)
			}
			if id != uint64(i) {
				b.Fatalf("got page ID %d, want %d", id, i)
			}
		}
	}
}

func newPagerForBenchmark(b *testing.B, pageCount int) *Pager {
	b.Helper()
	path := filepath.Join(b.TempDir(), "database.db")
	contents := bytes.Repeat([]byte{0x11}, pageCount*pagerByteSize)
	if err := os.WriteFile(path, contents, 0600); err != nil {
		b.Fatal(err)
	}
	pager, err := OpenPager(path)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := pager.Close(); err != nil {
			b.Error(err)
		}
	})
	return pager
}
