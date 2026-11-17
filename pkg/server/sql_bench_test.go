package server

import (
	"context"
	"testing"
	"time"
	"uuid"
)

func BenchmarkServerSQL(b *testing.B) {
	c := setupCluster(b, 1)
	c.start(b, 1)
	s := c.members[1]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for {
		state, err := s.Status()
		if err != nil {
			b.Fatal(err)
		}

		if state.IsLeader {
			break
		}

		select {
		case <-ctx.Done():
			b.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}

	create := prepareSQL(b, s, "CREATE TABLE bench (id BIGINT PRIMARY KEY)")
	executeSQL(b, s, uuid.UUID{1}, create)
	insert := prepareSQL(b, s, "INSERT INTO bench (id) VALUES (1)")
	executeSQL(b, s, uuid.UUID{2}, insert)
	read := prepareSQL(b, s, "SELECT * FROM bench WHERE id=1")
	b.Run("PrepareSelect", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			ctx, stop := context.WithTimeout(context.Background(), time.Second)
			_, err := s.PrepareSQL(ctx, "SELECT * FROM bench WHERE id=1")
			stop()

			if err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ExecuteSelect", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			ctx, stop := context.WithTimeout(context.Background(), time.Second)
			_, err := s.ExecuteSQL(ctx, uuid.Nil(), read)
			stop()

			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkServerMySQLRead(b *testing.B)         { benchmarkMySQLRead(b, false) }
func BenchmarkServerMySQLPreparedRead(b *testing.B) { benchmarkMySQLRead(b, true) }

func benchmarkMySQLRead(b *testing.B, prepared bool) {
	c := setupCluster(b, 1)
	enableMySQL(b, c, 1)
	c.start(b, 1)
	s := c.members[1]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for {
		state, err := s.Status()
		if err != nil {
			b.Fatal(err)
		}

		if state.IsLeader {
			break
		}

		select {
		case <-ctx.Done():
			b.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}

	db := mysqlClient(b, c, 1, "godb", "test-password", "godb")
	if _, err := db.Exec("CREATE TABLE bench (id BIGINT PRIMARY KEY)"); err != nil {
		b.Fatal(err)
	}

	if _, err := db.Exec("INSERT INTO bench (id) VALUES (1)"); err != nil {
		b.Fatal(err)
	}

	read := func(id *int64) error { return db.QueryRow("SELECT id FROM bench WHERE id=1").Scan(id) }

	if prepared {
		stmt, err := db.Prepare("SELECT id FROM bench WHERE id=?")
		if err != nil {
			b.Fatal(err)
		}

		defer func() {
			if err := stmt.Close(); err != nil {
				b.Error(err)
			}
		}()
		read = func(id *int64) error { return stmt.QueryRow(int64(1)).Scan(id) }
	}

	b.ReportAllocs()

	for b.Loop() {
		var id int64

		if err := read(&id); err != nil {
			b.Fatal(err)
		}
	}
}
