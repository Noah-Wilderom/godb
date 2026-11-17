package sql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func BenchmarkSQLDelete(b *testing.B) {
	for _, mode := range []string{"present", "missing"} {
		b.Run(mode, func(b *testing.B) {
			db, _ := openSQLDatabase(b)
			engine, err := NewLocal(db)
			require.NoError(b, err)
			execute(b, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,name TEXT UNIQUE)")
			b.ReportAllocs()

			for b.Loop() {
				if mode == "present" {
					b.StopTimer()
					execute(b, engine, "INSERT INTO t(id,name) VALUES(1,'one')")
					b.StartTimer()
				}

				_, err := engine.Execute(context.Background(), "DELETE FROM t WHERE id=1")
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
