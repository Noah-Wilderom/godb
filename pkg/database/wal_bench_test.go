package database

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/pager"
)

func BenchmarkWALCommit(b *testing.B) {
	for _, count := range []uint64{2, 16, 128} {
		b.Run(fmt.Sprintf("Pages%d", count), func(b *testing.B) {
			path := writeDatabaseFixture(b, validDatabaseBytes())
			wal, err := openWAL(path)
			require.NoError(b, err)
			b.Cleanup(func() { require.NoError(b, wal.file.Close()) })
			header := pager.NewDatabaseHeader()
			header.PageCount = count
			page, err := pager.EncodeHeader(header)
			require.NoError(b, err)
			pages := map[uint64][]byte{0: page}

			for id := uint64(1); id < count; id++ {
				pages[id] = validDatabaseBytes()[4096:]
			}

			b.ReportAllocs()

			for b.Loop() {
				if err := wal.commit(pages, count*4096); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
