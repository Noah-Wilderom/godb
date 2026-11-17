package sql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func BenchmarkSQLPrimaryRange(b *testing.B) {
	db, _ := openSQLDatabase(b)
	engine, err := NewLocal(db)
	require.NoError(b, err)
	execute(b, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,mirror BIGINT)")

	for id := range int64(1024) {
		execute(b, engine, "INSERT INTO t(id,mirror) VALUES(?,?)", record.NewInt64(id), record.NewInt64(id))
	}

	for _, test := range []struct{ name, query string }{
		{"seek_24", "SELECT COUNT(*) FROM t WHERE id>=1000"},
		{"filter_1024", "SELECT COUNT(*) FROM t WHERE mirror>=1000"},
		{"empty_seek", "SELECT COUNT(*) FROM t WHERE id>2000"},
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
