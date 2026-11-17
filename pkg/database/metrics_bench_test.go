package database

import "testing"

func BenchmarkGetInstrumentation(b *testing.B) {
	for _, mode := range []string{"Enabled", "CountersOnly"} {
		b.Run(mode, func(b *testing.B) {
			db, _ := openIdentityDatabase(b)

			key := []byte("key")
			if err := db.Put(key, []byte("value")); err != nil {
				b.Fatal(err)
			}

			if mode == "CountersOnly" {
				db.metrics = nil
			}

			b.ReportAllocs()

			for b.Loop() {
				if _, _, err := db.Get(key); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
