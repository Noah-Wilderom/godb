package pager

import (
	"fmt"
	"testing"
)

func BenchmarkBPlusInternal_Split(b *testing.B) {
	for _, tc := range []struct {
		name  string
		sizes []int
		parts int
	}{
		{"AlreadyFits", []int{3, 4, 5}, 1},
		{"ExactFit", []int{2000, 2065}, 1},
		{"TwoNodes", []int{2000, 2100, 100}, 2},
		{"ThreeNodes", []int{3000, 3000, 3000, 3000, 3000}, 3},
	} {
		b.Run(tc.name, func(b *testing.B) {
			node := internalSplitFixture(b, tc.sizes)
			b.ReportAllocs()
			for b.Loop() {
				nodes, separators, err := node.Split()
				if err != nil {
					b.Fatal(err)
				}
				if len(nodes) != tc.parts || len(separators) != tc.parts-1 {
					b.Fatal("unexpected split result")
				}
			}
		})
	}
}

func BenchmarkBPlusInternal_InsertSplit(b *testing.B) {
	for _, position := range []struct {
		name  string
		index int
	}{{"First", 0}, {"Middle", 64}, {"Last", 128}} {
		for _, added := range []int{1, 2} {
			b.Run(fmt.Sprintf("Keys128/%s/Add%d", position.name, added), func(b *testing.B) {
				keys, children := internalFixture(128)
				base, err := NewBPlusInternal(keys, children)
				if err != nil {
					b.Fatal(err)
				}
				separators := make([][]byte, added)
				rightChildren := make([]uint64, added)
				for i := range separators {
					separators[i] = []byte(fmt.Sprintf("%08d%c", 2*position.index+1, 'a'+i))
					rightChildren[i] = uint64(1000 + i)
				}
				b.ReportAllocs()
				for b.Loop() {
					node := *base
					if err := node.InsertSplit(children[position.index], separators, rightChildren); err != nil {
						b.Fatal(err)
					}
					if len(node.keys) != 128+added {
						b.Fatal("unexpected separator count")
					}
				}
			})
		}
	}
}

func BenchmarkNewBPlusInternal(b *testing.B) {
	for _, count := range []int{0, 1, 16, 128, 256} {
		b.Run(fmt.Sprintf("Keys%d", count), func(b *testing.B) {
			keys, children := internalFixture(count)
			b.ReportAllocs()
			for b.Loop() {
				if _, err := NewBPlusInternal(keys, children); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkBPlusInternal_ChildFor(b *testing.B) {
	for _, count := range []int{0, 1, 16, 128, 256} {
		for _, mode := range []string{"Equal", "Between", "Below", "Above"} {
			if count == 0 && mode != "Above" {
				continue
			}
			b.Run(fmt.Sprintf("Keys%d/%s", count, mode), func(b *testing.B) {
				keys, children := internalFixture(count)
				node, err := NewBPlusInternal(keys, children)
				if err != nil {
					b.Fatal(err)
				}
				queries := make([][]byte, 0, count+1)
				want := make([]uint64, 0, count+1)
				switch mode {
				case "Equal":
					queries = keys
					want = children[1:]
				case "Between":
					for i := 0; i <= count; i++ {
						queries = append(queries, []byte(fmt.Sprintf("%08d", 2*i+1)))
						want = append(want, children[i])
					}
				case "Below":
					queries = append(queries, []byte("00000000"))
					want = append(want, children[0])
				case "Above":
					queries = append(queries, []byte("99999999"))
					want = append(want, children[count])
				}
				b.ReportAllocs()
				i := 0
				for b.Loop() {
					if got := node.ChildFor(queries[i]); got != want[i] {
						b.Fatalf("got child %d, want %d", got, want[i])
					}
					i++
					if i == len(queries) {
						i = 0
					}
				}
			})
		}
	}
}
