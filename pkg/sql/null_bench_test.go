package sql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func BenchmarkSQLNullPredicates(b *testing.B) {
	db, _ := openSQLDatabase(b)
	engine, err := NewLocal(db)
	require.NoError(b, err)
	execute(b, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,token TEXT UNIQUE)")

	for id := range int64(256) {
		execute(b, engine, "INSERT INTO t(id) VALUES(?)", record.NewInt64(id))
	}

	for _, test := range []struct{ name, query string }{
		{"nullable_unique_scan", "SELECT COUNT(*) FROM t WHERE token IS NULL"},
		{"not_null_table_scan", "SELECT COUNT(*) FROM t WHERE token IS NOT NULL"},
		{"primary_filter", "SELECT id FROM t WHERE id=1 AND token IS NULL"},
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
