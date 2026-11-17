package sql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func BenchmarkSQLComparison(b *testing.B) {
	db, _ := openSQLDatabase(b)
	engine, err := NewLocal(db)
	require.NoError(b, err)
	execute(b, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,bucket BIGINT,INDEX by_bucket(bucket))")

	for id := range int64(256) {
		execute(b, engine, "INSERT INTO t(id,bucket) VALUES(?,?)", record.NewInt64(id), record.NewInt64(id%8))
	}

	for _, test := range []struct{ name, query string }{
		{"table", "SELECT COUNT(*) FROM t WHERE id>=128 AND id<192"},
		{"index_prefix", "SELECT COUNT(*) FROM t WHERE bucket=1 AND id>=128"},
		{"primary", "SELECT id FROM t WHERE id=129 AND id>=128"},
	} {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				if _, err := engine.Execute(context.Background(), test.query); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
