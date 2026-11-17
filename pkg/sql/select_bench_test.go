package sql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func BenchmarkSQLSelectSecondary(b *testing.B) {
	db, _ := openSQLDatabase(b)
	engine, err := NewLocal(db)
	require.NoError(b, err)
	execute(b, engine, "CREATE TABLE indexed(id BIGINT PRIMARY KEY,token BIGINT UNIQUE,bucket BIGINT,tenant BIGINT,active BOOL,KEY bucket_idx(bucket),KEY composite(tenant,bucket),UNIQUE KEY pair(bucket,token))")

	for id := range int64(32) {
		execute(b, engine, "INSERT INTO indexed(id,token,bucket,tenant,active) VALUES (?,?,1,1,TRUE)", record.NewInt64(id), record.NewInt64(id))
	}

	for _, test := range []struct{ name, query string }{
		{"unique", "SELECT id FROM indexed WHERE token=1"},
		{"nonunique_32", "SELECT id FROM indexed WHERE bucket=1"},
		{"composite_prefix_32", "SELECT id FROM indexed WHERE tenant=1"},
		{"residual_filter_32", "SELECT id FROM indexed WHERE tenant=1 AND active=FALSE"},
		{"composite_full_32", "SELECT id FROM indexed WHERE tenant=1 AND bucket=1"},
		{"composite_unique", "SELECT id FROM indexed WHERE bucket=1 AND token=1"},
		{"ordered_limit_32", "SELECT id FROM indexed WHERE tenant=1 ORDER BY token DESC,id LIMIT 5 OFFSET 5"},
	} {
		b.Run(test.name, func(b *testing.B) {
			for b.Loop() {
				if _, err := engine.Execute(context.Background(), test.query); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
