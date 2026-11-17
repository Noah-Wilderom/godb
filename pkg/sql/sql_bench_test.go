package sql

import (
	"context"
	"testing"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func BenchmarkSQLParseSelect(b *testing.B) {
	b.ReportAllocs()

	for b.Loop() {
		if _, err := Parse("SELECT id, name FROM users WHERE id = ?"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSQLSelectPrimaryKey(b *testing.B) {
	db, _ := openSQLDatabase(b)

	engine, err := NewLocal(db)
	if err != nil {
		b.Fatal(err)
	}

	execute(b, engine, "CREATE TABLE users (id BIGINT PRIMARY KEY, name TEXT)")
	execute(b, engine, "INSERT INTO users (id,name) VALUES (1,'example')")
	b.ReportAllocs()

	for b.Loop() {
		if _, err := engine.Execute(context.Background(), "SELECT name FROM users WHERE id=?", record.NewInt64(1)); err != nil {
			b.Fatal(err)
		}
	}
}
