package pager

import (
	"fmt"
	"testing"
)

func BenchmarkEncodeInternal(b *testing.B) {
	for _, count := range []int{0, 1, 16, 128, 226} {
		b.Run(fmt.Sprintf("Keys%d", count), func(b *testing.B) {
			keys, children := internalFixture(count)
			node, err := NewBPlusInternal(keys, children)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := EncodeInternal(node); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkDecodeInternal(b *testing.B) {
	for _, count := range []int{0, 1, 16, 128, 226} {
		b.Run(fmt.Sprintf("Keys%d", count), func(b *testing.B) {
			keys, children := internalFixture(count)
			page, err := EncodeInternal(&BPlusInternal{keys: keys, children: children})
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := DecodeInternal(page); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkEncodeLeaf(b *testing.B) {
	for _, count := range []int{0, 1, 128, 255} {
		b.Run(fmt.Sprintf("Entries%d", count), func(b *testing.B) {
			leaf := fixtureLeaf(count, 4)

			b.ReportAllocs()

			// Measures validation and allocation of a fresh encoded page.
			for b.Loop() {
				_, err := EncodeLeaf(leaf)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkDecodeLeaf(b *testing.B) {
	for _, count := range []int{0, 1, 128, 255} {
		b.Run(fmt.Sprintf("Entries%d", count), func(b *testing.B) {
			leaf := fixtureLeaf(count, 4)
			page, err := EncodeLeaf(leaf)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()

			// Encoding above is setup. Each operation decodes and validates a
			// new leaf, including allocation of its keys and values.
			for b.Loop() {
				_, err := DecodeLeaf(page)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkEncodeHeader(b *testing.B) {
	header := NewDatabaseHeader()
	b.ReportAllocs()

	// Includes validation and allocation of the entire 4096-byte page.
	for b.Loop() {
		_, err := EncodeHeader(header)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeHeader(b *testing.B) {
	page, err := EncodeHeader(NewDatabaseHeader())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()

	// Setup encoding is excluded; every decode checks both reserved regions.
	for b.Loop() {
		_, err := DecodeHeader(page)
		if err != nil {
			b.Fatal(err)
		}
	}
}
