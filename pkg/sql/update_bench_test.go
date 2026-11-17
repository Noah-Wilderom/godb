package sql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func BenchmarkSQLUpdate(b *testing.B) {
	for _, mode := range []string{"changed", "unchanged", "missing"} {
		b.Run(mode, func(b *testing.B) {
			db, _ := openSQLDatabase(b)
			engine, err := NewLocal(db)
			require.NoError(b, err)
			execute(b, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,value BIGINT,KEY by_value(value))")
			execute(b, engine, "INSERT INTO t(id,value) VALUES(1,0)")

			key := record.NewInt64(1)
			if mode == "missing" {
				key = record.NewInt64(99)
			}

			number := int64(0)
			b.ReportAllocs()

			for b.Loop() {
				if mode == "changed" {
					number = 1 - number
				}

				_, err := engine.Execute(context.Background(), "UPDATE t SET value=? WHERE id=?", record.NewInt64(number), key)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
