package pager

import (
	"fmt"
	"testing"
)

func BenchmarkValidateInternal(b *testing.B) {
	for _, count := range []int{0, 1, 16, 128, 226} {
		b.Run(fmt.Sprintf("Keys%d", count), func(b *testing.B) {
			keys, children := internalFixture(count)
			node, err := NewBPlusInternal(keys, children)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if err := ValidateInternal(node); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkValidateLeaf(b *testing.B) {
	for _, count := range []int{0, 1, 128, 255} {
		b.Run(fmt.Sprintf("Entries%d", count), func(b *testing.B) {
			leaf := fixtureLeaf(count, 4)

			b.ReportAllocs()

			for b.Loop() {
				if err := ValidateLeaf(leaf); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
