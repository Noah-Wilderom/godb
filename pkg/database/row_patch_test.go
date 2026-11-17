package database

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"testing"
	"uuid"

	"github.com/stretchr/testify/require"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func patchCommand(t testing.TB, table schema.Table, key recordpkg.Value, changes schema.Row) []byte {
	t.Helper()
	encoded, err := EncodeCommand(Command{Operations: []CommandOperation{{Kind: CommandPatchRow, Table: &table, PrimaryKey: &key, Row: changes}}})
	require.NoError(t, err)
	return encoded
}

func TestPatchCommandAtomicIndexesAndCounts(t *testing.T) {
	db, path := openIdentityDatabase(t)
	table, _ := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	require.NoError(t, db.InsertRow(table, numberedRow(1)))
	require.NoError(t, db.InsertRow(table, numberedRow(2)))
	key := recordpkg.NewUint64(1)
	encoded := patchCommand(t, table, key, schema.Row{2: recordpkg.NewBytes([]byte("renamed"))})
	result, err := db.ApplyCommand(encoded)
	require.NoError(t, err)
	require.Equal(t, CommandResult{Code: CommandOK, AffectedRows: 1, MatchedRows: 1}, result)
	want := numberedRow(1)
	want[2] = recordpkg.NewBytes([]byte("renamed"))
	assertStoredRow(t, db, table, want)
	_, found, err := db.GetRowByUniqueIndex(table, 7, numberedRow(1)[2])
	require.NoError(t, err)
	require.False(t, found)
	result, err = db.ApplyCommand(encoded)
	require.NoError(t, err)
	require.Equal(t, CommandResult{Code: CommandOK, MatchedRows: 1}, result)

	for _, key := range []recordpkg.Value{recordpkg.NewUint64(999), recordpkg.NewNull()} {
		result, err = db.ApplyCommand(patchCommand(t, table, key, schema.Row{3: recordpkg.NewInt64(99)}))
		require.NoError(t, err)
		require.Equal(t, CommandResult{Code: CommandOK}, result)
	}

	key2 := recordpkg.NewUint64(2)
	command := Command{Operations: []CommandOperation{
		{Kind: CommandPatchRow, Table: &table, PrimaryKey: &key, Row: schema.Row{3: recordpkg.NewInt64(99)}},
		{Kind: CommandPatchRow, Table: &table, PrimaryKey: &key2, Row: schema.Row{2: want[2]}},
	}}
	encoded, err = EncodeCommand(command)
	require.NoError(t, err)
	result, err = db.ApplyCommand(encoded)
	require.NoError(t, err)
	require.Equal(t, CommandResult{Code: CommandUniqueConflict}, result)
	assertStoredRow(t, db, table, want)
	result, err = db.ApplyCommand(patchCommand(t, table, key, schema.Row{2: recordpkg.NewBytes(bytes.Repeat([]byte("x"), 5000))}))
	require.NoError(t, err)
	require.Equal(t, CommandResult{Code: CommandInvalidUpdate}, result)
	assertStoredRow(t, db, table, want)
	result, err = db.ApplyCommand(patchCommand(t, table, key, schema.Row{2: recordpkg.NewNull()}))
	require.NoError(t, err)
	require.Equal(t, uint64(1), result.AffectedRows)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestPatchCommandCodecValidation(t *testing.T) {
	table, _ := rowInsertFixture()

	key := recordpkg.NewUint64(1)
	for _, changes := range []schema.Row{nil, {}, {1: key}, {9: recordpkg.NewInt64(1)}, {3: recordpkg.NewNull()}, {3: recordpkg.NewBool(true)}} {
		_, err := EncodeCommand(Command{Operations: []CommandOperation{{Kind: CommandPatchRow, Table: &table, PrimaryKey: &key, Row: changes}}})
		require.ErrorIs(t, err, ErrInvalidCommand)
	}

	encoded := patchCommand(t, table, key, schema.Row{2: recordpkg.NewNull(), 3: recordpkg.NewInt64(99)})
	command, err := DecodeCommand(encoded)
	require.NoError(t, err)
	again, err := EncodeCommand(command)
	require.NoError(t, err)
	require.Equal(t, encoded, again)

	for length := range len(encoded) {
		_, err := DecodeCommand(encoded[:length])
		require.ErrorIs(t, err, ErrInvalidCommand)
	}
}

func TestPatchLogCountsSurviveRetryReopenAndConflicts(t *testing.T) {
	db, path := openIdentityDatabase(t)
	table, _ := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	require.NoError(t, db.InsertRow(table, numberedRow(1)))
	key := recordpkg.NewUint64(1)
	first := testLogEntry(t, 1, Command{Operations: []CommandOperation{{Kind: CommandPatchRow, Table: &table, PrimaryKey: &key, Row: schema.Row{3: recordpkg.NewInt64(99)}}}})
	result, err := db.ApplyLogEntry(first)
	require.NoError(t, err)
	require.Equal(t, CommandResult{Code: CommandOK, AffectedRows: 1, MatchedRows: 1}, result)
	noop := first
	noop.Index = 2
	noop.RequestID = uuid.NewV4()
	noChange, err := db.ApplyLogEntry(noop)
	require.NoError(t, err)
	require.Equal(t, CommandResult{Code: CommandOK, MatchedRows: 1}, noChange)
	later := testLogEntry(t, 3, Command{Operations: []CommandOperation{{Kind: CommandPatchRow, Table: &table, PrimaryKey: &key, Row: schema.Row{3: recordpkg.NewInt64(100)}}}})
	_, err = db.ApplyLogEntry(later)
	require.NoError(t, err)
	retry := first
	retry.Index = 4
	replay, err := db.ApplyLogEntry(retry)
	require.NoError(t, err)
	require.Equal(t, result, replay)
	row, found, err := db.GetRow(table, key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, recordpkg.NewInt64(100), row[3])
	conflict := later
	conflict.Index = 5
	conflict.RequestID = first.RequestID
	rejected, err := db.ApplyLogEntry(conflict)
	require.NoError(t, err)
	require.Equal(t, CommandResult{Code: CommandRequestIDConflict}, rejected)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
	reopened, err := Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()

	for _, entry := range []LogEntry{first, noop, retry} {
		got, err := reopened.ApplyLogEntry(entry)
		require.NoError(t, err)

		if entry.RequestID == noop.RequestID {
			require.Equal(t, noChange, got)
		} else {
			require.Equal(t, result, got)
		}
	}

	stored, found, err := reopened.AppliedRequest(first.RequestID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, result, stored)
}

func TestReplicationMetadataRowCounts(t *testing.T) {
	for _, affected := range []uint64{0, 1} {
		log := logRecord{term: 1, requestID: uuid.NewV4(), digest: sha256.Sum256(nil), code: CommandOK, affectedRows: affected, matchedRows: 1}
		encoded := encodeLogRecord(log)
		require.Equal(t, byte(2), encoded[0])
		require.Len(t, encoded, 74)
		decoded, err := decodeLogRecord(encoded)
		require.NoError(t, err)
		require.Equal(t, log, decoded)
		request := requestRecord{index: 1, digest: log.digest, code: CommandOK, affectedRows: affected, matchedRows: 1}
		stored := encodeRequestRecord(request)
		require.Len(t, stored, 58)
		got, err := decodeRequestRecord(stored)
		require.NoError(t, err)
		require.Equal(t, request, got)

		for _, mode := range []string{"zero_matched", "too_many", "affected_exceeds", "rejection", "version", "truncated"} {
			bad := bytes.Clone(encoded)

			switch mode {
			case "zero_matched":
				binary.LittleEndian.PutUint64(bad[66:], 0)
			case "too_many":
				binary.LittleEndian.PutUint64(bad[66:], MaxBatchEntries+1)
			case "affected_exceeds":
				binary.LittleEndian.PutUint64(bad[58:], 2)
			case "rejection":
				bad[57] = encodeResultCode(CommandUniqueConflict)
			case "version":
				bad[0] = 1
			case "truncated":
				bad = bad[:73]
			}

			_, err := decodeLogRecord(bad)
			require.ErrorIs(t, err, ErrInvalidReplicationState, mode)
		}
	}
}
