package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func BenchmarkCommandCodec(b *testing.B) {
	command := commandFixture()
	encoded, err := EncodeCommand(command)
	require.NoError(b, err)
	b.Run("Encode", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			if _, err := EncodeCommand(command); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Decode", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			if _, err := DecodeCommand(encoded); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkApplyCommand(b *testing.B) {
	db, _ := openIdentityDatabase(b)
	table, row := rowInsertFixture()
	require.NoError(b, db.CreateTable(table))
	key := row[1]
	encoded, err := EncodeCommand(Command{Operations: []CommandOperation{
		{Kind: CommandInsertRow, Table: &table, Row: row},
		{Kind: CommandDeleteRow, Table: &table, PrimaryKey: &key},
	}})
	require.NoError(b, err)
	b.ReportAllocs()

	for b.Loop() {
		result, err := db.ApplyCommand(encoded)
		if err != nil || result.Code != CommandOK {
			b.Fatalf("result=%v err=%v", result, err)
		}
	}
}
