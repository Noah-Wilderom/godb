package database

import (
	"testing"
	"uuid"

	"github.com/stretchr/testify/require"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func countedDeleteCommand(t testing.TB, table schema.Table, key recordpkg.Value) []byte {
	t.Helper()
	encoded, err := EncodeCommand(Command{Operations: []CommandOperation{{Kind: CommandDeleteRowCounted, Table: &table, PrimaryKey: &key}}})
	require.NoError(t, err)
	return encoded
}

func TestCountedDeleteAtomicIndexesAndLegacyBehavior(t *testing.T) {
	db, path := openIdentityDatabase(t)
	table, _ := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	first, second := numberedRow(1), numberedRow(2)
	require.NoError(t, db.InsertRow(table, first))
	require.NoError(t, db.InsertRow(table, second))
	key := first[1]
	rejected, err := EncodeCommand(Command{Operations: []CommandOperation{
		{Kind: CommandDeleteRowCounted, Table: &table, PrimaryKey: &key},
		{Kind: CommandInsertRow, Table: &table, Row: second},
	}})
	require.NoError(t, err)
	result, err := db.ApplyCommand(rejected)
	require.NoError(t, err)
	require.Equal(t, CommandResult{Code: CommandDuplicatePrimaryKey}, result)
	assertStoredRow(t, db, table, first)
	encoded := countedDeleteCommand(t, table, key)
	result, err = db.ApplyCommand(encoded)
	require.NoError(t, err)
	require.Equal(t, CommandResult{Code: CommandOK, AffectedRows: 1, MatchedRows: 1}, result)
	_, found, err := db.GetRow(table, key)
	require.NoError(t, err)
	require.False(t, found)

	for _, index := range table.Indexes {
		values := make([]recordpkg.Value, 0, len(index.ColumnIDs))
		for _, id := range index.ColumnIDs {
			values = append(values, first[id])
		}

		rows, err := db.ScanRowsByIndex(table, index.ID, 10, values...)
		require.NoError(t, err)
		require.Empty(t, rows)
	}

	for _, key := range []recordpkg.Value{key, recordpkg.NewUint64(999), recordpkg.NewNull()} {
		result, err = db.ApplyCommand(countedDeleteCommand(t, table, key))
		require.NoError(t, err)
		require.Equal(t, CommandResult{Code: CommandOK}, result)
	}

	assertStoredRow(t, db, table, second)
	require.NoError(t, db.InsertRow(table, first))
	legacy, err := EncodeCommand(Command{Operations: []CommandOperation{{Kind: CommandDeleteRow, Table: &table, PrimaryKey: &key}}})
	require.NoError(t, err)
	result, err = db.ApplyCommand(legacy)
	require.NoError(t, err)
	require.Equal(t, CommandResult{Code: CommandOK}, result)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestCountedDeleteCommandCodec(t *testing.T) {
	table, _ := rowInsertFixture()
	for _, key := range []recordpkg.Value{recordpkg.NewUint64(1), recordpkg.NewNull()} {
		encoded := countedDeleteCommand(t, table, key)
		decoded, err := DecodeCommand(encoded)
		require.NoError(t, err)
		require.Equal(t, CommandDeleteRowCounted, decoded.Operations[0].Kind)
		require.Equal(t, key, *decoded.Operations[0].PrimaryKey)
		again, err := EncodeCommand(decoded)
		require.NoError(t, err)
		require.Equal(t, encoded, again)

		for length := range len(encoded) {
			_, err := DecodeCommand(encoded[:length])
			require.ErrorIs(t, err, ErrInvalidCommand)
		}
	}

	key := recordpkg.NewUint64(1)
	for _, op := range []CommandOperation{
		{Kind: CommandDeleteRowCounted, Table: &table},
		{Kind: CommandDeleteRowCounted, PrimaryKey: &key},
		{Kind: CommandDeleteRowCounted, Table: &table, PrimaryKey: &key, Row: schema.Row{}},
		{Kind: CommandDeleteRowCounted, Table: &table, PrimaryKey: &key, Value: []byte("extra")},
	} {
		_, err := EncodeCommand(Command{Operations: []CommandOperation{op}})
		require.ErrorIs(t, err, ErrInvalidCommand)
	}

	wrong := recordpkg.NewInt64(1)
	_, err := EncodeCommand(Command{Operations: []CommandOperation{{Kind: CommandDeleteRowCounted, Table: &table, PrimaryKey: &wrong}}})
	require.ErrorIs(t, err, ErrInvalidCommand)
	null := recordpkg.NewNull()
	_, err = EncodeCommand(Command{Operations: []CommandOperation{{Kind: CommandDeleteRow, Table: &table, PrimaryKey: &null}}})
	require.ErrorIs(t, err, ErrInvalidCommand)
}

func TestCountedDeleteLogRetryPreservesReplacement(t *testing.T) {
	db, path := openIdentityDatabase(t)
	table, _ := rowInsertFixture()
	row := numberedRow(1)
	require.NoError(t, db.CreateTable(table))
	require.NoError(t, db.InsertRow(table, row))
	key := row[1]
	first := testLogEntry(t, 1, Command{Operations: []CommandOperation{{Kind: CommandDeleteRowCounted, Table: &table, PrimaryKey: &key}}})
	result, err := db.ApplyLogEntry(first)
	require.NoError(t, err)
	require.Equal(t, CommandResult{Code: CommandOK, AffectedRows: 1, MatchedRows: 1}, result)
	missing := first
	missing.Index = 2
	missing.RequestID = uuid.NewV4()
	empty, err := db.ApplyLogEntry(missing)
	require.NoError(t, err)
	require.Equal(t, CommandResult{Code: CommandOK}, empty)
	replacement := numberedRow(1)
	replacement[2] = recordpkg.NewBytes([]byte("replacement"))
	insert := testLogEntry(t, 3, Command{Operations: []CommandOperation{{Kind: CommandInsertRow, Table: &table, Row: replacement}}})
	assertLogResult(t, db, insert, CommandOK)
	retry := first
	retry.Index = 4
	replay, err := db.ApplyLogEntry(retry)
	require.NoError(t, err)
	require.Equal(t, result, replay)
	retryMissing := missing
	retryMissing.Index = 5
	replay, err = db.ApplyLogEntry(retryMissing)
	require.NoError(t, err)
	require.Equal(t, empty, replay)
	assertStoredRow(t, db, table, replacement)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
	reopened, err := Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()

	for _, entry := range []LogEntry{first, missing, retry, retryMissing} {
		got, err := reopened.ApplyLogEntry(entry)
		require.NoError(t, err)

		if entry.RequestID == first.RequestID {
			require.Equal(t, result, got)
		} else {
			require.Equal(t, empty, got)
		}
	}

	assertStoredRow(t, reopened, table, replacement)
	stored, found, err := reopened.AppliedRequest(first.RequestID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, result, stored)
}
