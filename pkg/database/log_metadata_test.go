package database

import (
	"crypto/sha256"
	"encoding/binary"
	"testing"
	"uuid"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/pager"
)

func TestReplicationMetadataCodecs(t *testing.T) {
	position := LogPosition{GroupID: uuid.NewV4(), Index: 7, Term: 3}
	encoded := encodeLogPosition(position)
	decoded, err := decodeLogPosition(encoded)
	require.NoError(t, err)
	require.Equal(t, position, decoded)

	for _, code := range []CommandResultCode{
		CommandOK, CommandDuplicatePrimaryKey, CommandUniqueConflict, CommandTableIDExists,
		CommandTableNameExists, CommandTableNotFound, CommandSchemaMismatch, CommandMutationLimit, CommandRequestIDConflict, CommandInvalidUpdate,
	} {
		require.Equal(t, code, decodeResultCode(encodeResultCode(code)))
		record := logRecord{term: 3, requestID: uuid.NewV4(), digest: sha256.Sum256([]byte("command")), code: code}
		got, err := decodeLogRecord(encodeLogRecord(record))
		require.NoError(t, err)
		require.Equal(t, record, got)
		request := requestRecord{index: 7, digest: record.digest, code: code}

		gotRequest, err := decodeRequestRecord(encodeRequestRecord(request))
		if code == CommandRequestIDConflict {
			require.ErrorIs(t, err, ErrInvalidReplicationState)
		} else {
			require.NoError(t, err)
			require.Equal(t, request, gotRequest)
		}
	}

	require.Empty(t, decodeResultCode(255))
	require.Zero(t, encodeResultCode("unknown"))

	for length := range 33 {
		_, err := decodeLogPosition(encoded[:length])
		require.ErrorIs(t, err, ErrInvalidReplicationState)
	}
}

func TestValidateRejectsCorruptReplicationMetadata(t *testing.T) {
	for _, mode := range []string{"MissingState", "BadState", "MissingLog", "BadCode", "BadTerm", "MissingRequest", "BadRequestDigest", "Gap", "FutureLog", "UnknownKey"} {
		t.Run(mode, func(t *testing.T) {
			db, path := openIdentityDatabase(t)
			entry := testLogEntry(t, 1, Command{Operations: []CommandOperation{{Kind: CommandDelete, Key: []byte("missing")}}})
			assertLogResult(t, db, entry, CommandOK)
			assertLogResult(t, db, LogEntry{GroupID: entry.GroupID, Index: 2, Term: 1}, CommandOK)
			require.NoError(t, db.Close())
			p, err := pager.OpenPager(path)
			require.NoError(t, err)
			page, err := p.ReadPage(1)
			require.NoError(t, err)
			leaf, err := pager.DecodeLeaf(page)
			require.NoError(t, err)

			switch mode {
			case "MissingState":
				leaf.Delete(logPositionKey())
			case "BadState":
				leaf.Put(logPositionKey(), []byte{1})
			case "MissingLog":
				leaf.Delete(logRecordKey(2))
			case "BadCode":
				value, _, _ := leaf.Get(logRecordKey(2))
				value[57] = 255
				leaf.Put(logRecordKey(2), value)
			case "BadTerm":
				value, _, _ := leaf.Get(logRecordKey(2))
				binary.LittleEndian.PutUint64(value[1:9], 3)
				leaf.Put(logRecordKey(2), value)
			case "MissingRequest":
				leaf.Delete(requestRecordKey(entry.RequestID))
			case "BadRequestDigest":
				value, _, _ := leaf.Get(requestRecordKey(entry.RequestID))
				value[9] ^= 1
				leaf.Put(requestRecordKey(entry.RequestID), value)
			case "Gap":
				leaf.Delete(logRecordKey(1))
				leaf.Delete(requestRecordKey(entry.RequestID))
			case "FutureLog":
				value, _, _ := leaf.Get(logRecordKey(2))
				leaf.Put(logRecordKey(3), value)
			case "UnknownKey":
				leaf.Put([]byte{0, replicationNamespace, 255}, nil)
			}

			page, err = pager.EncodeLeaf(leaf)
			require.NoError(t, err)
			require.NoError(t, p.WritePage(1, page))
			require.NoError(t, p.Close())
			require.ErrorIs(t, Validate(path), ErrInvalidReplicationState)

			if mode == "MissingState" || mode == "BadState" || mode == "MissingLog" || mode == "BadCode" || mode == "BadTerm" {
				opened, err := Open(path)
				require.Nil(t, opened)
				require.ErrorIs(t, err, ErrInvalidReplicationState)
			}
		})
	}
}

func FuzzReplicationMetadataDecoders(f *testing.F) {
	f.Add(encodeLogRecord(logRecord{term: 1, requestID: uuid.NewV4(), code: CommandOK, affectedRows: 1, matchedRows: 1}))
	f.Add(encodeRequestRecord(requestRecord{index: 1, code: CommandOK, matchedRows: 1}))
	f.Add(encodeLogPosition(LogPosition{GroupID: uuid.NewV4(), Index: 1, Term: 1}))
	f.Add(encodeLogRecord(logRecord{term: 1, digest: sha256.Sum256(nil), code: CommandOK}))
	f.Add(encodeRequestRecord(requestRecord{index: 1, digest: sha256.Sum256(nil), code: CommandOK}))
	f.Fuzz(func(t *testing.T, data []byte) {
		if position, err := decodeLogPosition(data); err == nil {
			require.Equal(t, data, encodeLogPosition(position))
		}

		if record, err := decodeLogRecord(data); err == nil {
			require.Equal(t, data, encodeLogRecord(record))
		}

		if record, err := decodeRequestRecord(data); err == nil {
			require.Equal(t, data, encodeRequestRecord(record))
		}
	})
}
