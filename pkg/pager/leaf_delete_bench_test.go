package pager

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func BenchmarkBPlusLeaf_PutDeleteCycle(b *testing.B) {
	leaf, err := NewBPlusLeaf(byteStrings("a", "c", "e"), byteStrings("one", "two", "three"))
	require.NoError(b, err)
	key, value := []byte("b"), []byte("value")
	b.ReportAllocs()

	for b.Loop() {
		leaf.Put(key, value)

		if !leaf.Delete(key) {
			b.Fatal("key was not deleted")
		}
	}
}
