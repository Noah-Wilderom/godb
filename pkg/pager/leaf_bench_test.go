package pager

import "testing"

func BenchmarkBPlusLeaf_Get(b *testing.B) {
	leaf := fixtureLeaf(256, 16)
	b.ReportAllocs()

	i := 0
	for b.Loop() {
		if _, _, found := leaf.Get(leaf.keys[i]); !found {
			b.Fatal("key missing")
		}

		i = (i + 1) % 256
	}
}

func BenchmarkBPlusLeaf_PutReplace(b *testing.B) {
	leaf := fixtureLeaf(256, 16)
	value := []byte("replacement")
	b.ReportAllocs()

	i := 0
	for b.Loop() {
		leaf.Put(leaf.keys[i], value)
		i = (i + 1) % 256
	}
}

func BenchmarkBPlusLeaf_PutInsertBatch256(b *testing.B) {
	for _, order := range []string{"Ascending", "Descending", "Mixed"} {
		b.Run(order, func(b *testing.B) {
			fixture := fixtureLeaf(256, 16)

			keys := make([][]byte, 256)
			for i := range keys {
				index := i
				if order == "Descending" {
					index = 255 - i
				}

				if order == "Mixed" {
					index = (i * 73) % 256
				}

				keys[i] = fixture.keys[index]
			}

			b.ReportAllocs()

			for b.Loop() {
				leaf := BPlusLeaf{}
				for _, key := range keys {
					leaf.Put(key, fixture.values[0])
				}
			}
		})
	}
}
