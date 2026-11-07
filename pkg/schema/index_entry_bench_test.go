package schema

import (
	"testing"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func BenchmarkEncodeIndexEntry(b *testing.B) {
	cases := []struct {
		name      string
		unique    bool
		null      bool
		composite bool
	}{
		{"NonUnique", false, false, false},
		{"Unique", true, false, false},
		{"UniqueNull", true, true, false},
		{"Composite", false, false, true},
		{"UniqueComposite", true, false, true},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			table, row := indexEntryFixture(b, tc.unique)

			if tc.null {
				row[7] = record.NewNull()
			}

			if tc.composite {
				table.Indexes[0].ColumnIDs = []uint32{7, 100}
			}

			b.ReportAllocs()

			for b.Loop() {
				if _, err := EncodeIndexEntry(table, 3, row); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
