package replication

import (
	"encoding/binary"
	"testing"
	"uuid"
)

func BenchmarkNodePropose(b *testing.B) {
	for _, mode := range []string{"NewRequest", "RetryRequest"} {
		b.Run(mode, func(b *testing.B) {
			f := newFixture(b, 1)
			f.start(b, 1)

			if _, err := f.node.Campaign(); err != nil {
				b.Fatal(err)
			}

			command := commandPut(b, "key", "value")

			id := uuid.UUID{1}
			if _, err := f.node.Propose(id, command); err != nil {
				b.Fatal(err)
			}

			index := uint64(0)
			b.ReportAllocs()
			b.ResetTimer()

			for b.Loop() {
				if mode == "NewRequest" {
					index++
					binary.LittleEndian.PutUint64(id[8:], index)
				}

				if _, err := f.node.Propose(id, command); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkDecodeProposal(b *testing.B) {
	encoded, err := EncodeProposal(uuid.UUID{1}, commandPut(b, "key", "value"))
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()

	for b.Loop() {
		if _, _, err := DecodeProposal(encoded); err != nil {
			b.Fatal(err)
		}
	}
}
