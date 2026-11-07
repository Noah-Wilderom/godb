package database

import (
	"testing"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
)

func BenchmarkDatabase_ValueOperations(b *testing.B) {
	cases := []struct {
		name  string
		value recordpkg.Value
	}{
		{"Int64", recordpkg.NewInt64(42)},
		{"NestedMap", recordpkg.NewMap(map[string]recordpkg.Value{
			"id": recordpkg.NewInt64(42),
			"items": recordpkg.NewArray([]recordpkg.Value{
				recordpkg.NewBool(true), recordpkg.NewBytes(make([]byte, 64)),
			}),
		})},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			for _, write := range []bool{false, true} {
				name := "GetValueCached"
				if write {
					name = "PutValueSynced"
				}

				b.Run(name, func(b *testing.B) {
					db, _ := openWriterDatabase(b)

					key := []byte("key")
					if err := db.PutValue(key, tc.value); err != nil {
						b.Fatal(err)
					}

					b.ReportAllocs()

					for b.Loop() {
						if write {
							if err := db.PutValue(key, tc.value); err != nil {
								b.Fatal(err)
							}
						} else {
							value, found, err := db.GetValue(key)
							if err != nil || !found || value.Type() != tc.value.Type() {
								b.Fatal("unexpected value result", err)
							}
						}
					}
				})
			}
		})
	}
}
