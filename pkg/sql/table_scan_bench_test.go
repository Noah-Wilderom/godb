package sql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func BenchmarkSQLTableScan(b *testing.B) {
	db, _ := openSQLDatabase(b)
	engine, err := NewLocal(db)
	require.NoError(b, err)
	execute(b, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,active BOOL)")

	for id := range int64(256) {
		execute(b, engine, "INSERT INTO t(id,active) VALUES(?,?)", record.NewInt64(id), record.NewBool(id%2 == 0))
	}

	for _, test := range []struct{ name, query string }{
		{"all", "SELECT id FROM t"},
		{"filtered_sorted_limit", "SELECT id FROM t WHERE active=TRUE ORDER BY id DESC LIMIT 10"},
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
