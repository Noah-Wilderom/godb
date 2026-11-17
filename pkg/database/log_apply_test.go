package database

import (
	"bytes"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"uuid"

	"github.com/stretchr/testify/require"
)

func testLogEntry(t testing.TB, index uint64, command Command) LogEntry {
	t.Helper()
	encoded, err := EncodeCommand(command)
	require.NoError(t, err)
	return LogEntry{
		GroupID: uuid.MustParse("11111111-1111-4111-8111-111111111111"), Index: index, Term: 1,
		RequestID: uuid.NewV4(), Command: encoded,
	}
}

func assertLogResult(t testing.TB, db *Database, entry LogEntry, code CommandResultCode) {
	t.Helper()
	result, err := db.ApplyLogEntry(entry)
	require.NoError(t, err)
	require.Equal(t, code, result.Code)
}

func TestApplyLogEntryPersistsReplayAndRequestResults(t *testing.T) {
	db, path := openIdentityDatabase(t)
	first := testLogEntry(t, 1, commandFixture())
	assertLogResult(t, db, first, CommandOK)
	before := db.Stats()
	assertLogResult(t, db, first, CommandOK)
	require.Equal(t, before.PageWrites, db.Stats().PageWrites)
	require.Equal(t, before.Syncs, db.Stats().Syncs)
	retry := first
	retry.Index = 2
	assertLogResult(t, db, retry, CommandOK)
	require.Equal(t, before.Writes+2, db.Stats().Writes)
	duplicate := first
	duplicate.Index, duplicate.RequestID = 3, uuid.NewV4()
	assertLogResult(t, db, duplicate, CommandTableIDExists)
	conflict := testLogEntry(t, 4, Command{Operations: []CommandOperation{{Kind: CommandPut, Key: []byte("must-not-write")}}})
	conflict.RequestID = first.RequestID
	assertLogResult(t, db, conflict, CommandRequestIDConflict)
	_, found, err := db.Get([]byte("must-not-write"))
	require.NoError(t, err)
	require.False(t, found)
	noop := LogEntry{GroupID: first.GroupID, Index: 5, Term: 2}
	assertLogResult(t, db, noop, CommandOK)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
	db, err = Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	position, err := db.AppliedPosition()
	require.NoError(t, err)
	require.Equal(t, LogPosition{GroupID: first.GroupID, Index: 5, Term: 2}, position)

	for _, tc := range []struct {
		entry LogEntry
		code  CommandResultCode
	}{
		{first, CommandOK}, {retry, CommandOK}, {duplicate, CommandTableIDExists}, {conflict, CommandRequestIDConflict}, {noop, CommandOK},
	} {
		before := db.Stats()
		assertLogResult(t, db, tc.entry, tc.code)
		require.Equal(t, before.PageWrites, db.Stats().PageWrites)
	}

	for _, tc := range []struct {
		id   uuid.UUID
		code CommandResultCode
	}{{first.RequestID, CommandOK}, {duplicate.RequestID, CommandTableIDExists}} {
		result, found, err := db.AppliedRequest(tc.id)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, tc.code, result.Code)
	}
}

func TestApplyLogEntryRejectedAttemptDiscardsUserChangesButIsRemembered(t *testing.T) {
	db, path := openIdentityDatabase(t)
	table, row := rowInsertFixture()

	command := Command{}
	for _, entry := range crashEntries() {
		command.Operations = append(command.Operations, CommandOperation{Kind: CommandPut, Key: entry.Key, Value: entry.Value})
	}

	command.Operations = append(command.Operations, CommandOperation{Kind: CommandInsertRow, Table: &table, Row: row})
	rejected := testLogEntry(t, 1, command)
	assertLogResult(t, db, rejected, CommandTableNotFound)
	_, found, err := db.Get(crashEntries()[0].Key)
	require.NoError(t, err)
	require.False(t, found)
	assertLogResult(t, db, testLogEntry(t, 2, Command{Operations: []CommandOperation{{Kind: CommandCreateTable, Table: &table}}}), CommandOK)
	retry := rejected
	retry.Index = 3
	assertLogResult(t, db, retry, CommandTableNotFound)
	_, found, err = db.GetRow(table, row[1])
	require.NoError(t, err)
	require.False(t, found)
	retry.Index, retry.RequestID = 4, uuid.NewV4()
	assertLogResult(t, db, retry, CommandOK)
	assertStoredRow(t, db, table, row)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestApplyLogEntryRejectsInvalidOrderingAndEnvelope(t *testing.T) {
	db, _ := openIdentityDatabase(t)

	first := testLogEntry(t, 1, Command{Operations: []CommandOperation{{Kind: CommandPut, Key: []byte("key"), Value: []byte("one")}}})
	for _, mutate := range []func(*LogEntry){
		func(e *LogEntry) { e.GroupID = uuid.Nil() },
		func(e *LogEntry) { e.Index = 0 },
		func(e *LogEntry) { e.Term = 0 },
		func(e *LogEntry) { e.RequestID = uuid.Nil() },
		func(e *LogEntry) { e.Command = nil },
	} {
		bad := first
		mutate(&bad)
		_, err := db.ApplyLogEntry(bad)
		require.ErrorIs(t, err, ErrInvalidLogEntry)
	}

	gap := first
	gap.Index = ^uint64(0)
	_, err := db.ApplyLogEntry(gap)
	require.ErrorIs(t, err, ErrLogOrder)
	first.Term = 2
	assertLogResult(t, db, first, CommandOK)

	for _, tc := range []struct {
		mutate func(*LogEntry)
		want   error
	}{
		{func(e *LogEntry) { e.GroupID = uuid.NewV4() }, ErrLogGroupMismatch},
		{func(e *LogEntry) { e.Index = 3 }, ErrLogOrder},
		{func(e *LogEntry) { e.Index, e.Term = 2, 1 }, ErrLogOrder},
		{func(e *LogEntry) { e.Term = 3 }, ErrLogConflict},
		{func(e *LogEntry) { e.RequestID = uuid.NewV4() }, ErrLogConflict},
		{func(e *LogEntry) {
			e.Command = testLogEntry(t, 1, Command{Operations: []CommandOperation{{Kind: CommandDelete}}}).Command
		}, ErrLogConflict},
	} {
		bad := first
		tc.mutate(&bad)
		_, err := db.ApplyLogEntry(bad)
		require.ErrorIs(t, err, tc.want)
	}

	assertLogResult(t, db, LogEntry{GroupID: first.GroupID, Index: 2, Term: ^uint64(0)}, CommandOK)
}

func TestLogBindingPreventsLocalMutationBypasses(t *testing.T) {
	db, _ := openIdentityDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	tx := db.BeginWrite()
	require.NoError(t, tx.InsertRow(table, row))
	entry := testLogEntry(t, 1, Command{Operations: []CommandOperation{{Kind: CommandInsertRow, Table: &table, Row: row}}})
	assertLogResult(t, db, entry, CommandOK)

	for _, write := range []func() error{
		func() error { return db.Put([]byte("a"), nil) },
		func() error { _, err := db.Delete([]byte("a")); return err },
		func() error { return db.CreateTable(table) },
		func() error { return db.InsertRow(table, row) },
		func() error { _, err := db.UpdateRow(table, row); return err },
		func() error { _, err := db.DeleteRow(table, row[1]); return err },
		func() error { _, err := db.ApplyCommand(entry.Command); return err },
		tx.Commit,
	} {
		require.ErrorIs(t, write(), ErrReplicatedWriteRequired)
	}

	readonly := db.BeginWrite()
	_, found, err := readonly.GetRow(table, row[1])
	require.NoError(t, err)
	require.True(t, found)
	require.NoError(t, readonly.Commit())
}

func TestReplicationNamespaceCannotBeMutatedByClients(t *testing.T) {
	db, _ := openIdentityDatabase(t)
	for _, key := range [][]byte{{0, replicationNamespace}, logPositionKey(), logRecordKey(1), requestRecordKey(uuid.NewV4())} {
		require.ErrorIs(t, db.Put(key, nil), ErrReservedKey)
		_, err := db.Delete(key)
		require.ErrorIs(t, err, ErrReservedKey)
		tx := db.BeginWrite()
		require.ErrorIs(t, tx.Put(key, nil), ErrReservedKey)
		require.ErrorIs(t, tx.Commit(), ErrReservedKey)
		_, err = EncodeCommand(Command{Operations: []CommandOperation{{Kind: CommandPut, Key: key}}})
		require.ErrorIs(t, err, ErrInvalidCommand)
		require.ErrorIs(t, err, ErrReservedKey)
	}
}

func TestApplyLogEntryConcurrentRetryWritesOnce(t *testing.T) {
	db, _ := openIdentityDatabase(t)
	entry := testLogEntry(t, 1, commandFixture())
	results := make(chan error, 16)

	var wg sync.WaitGroup

	for range 16 {
		wg.Go(func() {
			result, err := db.ApplyLogEntry(entry)
			if err == nil && result.Code != CommandOK {
				err = errors.New("retry changed the result")
			}

			results <- err
		})
	}

	wg.Wait()

	for range 16 {
		require.NoError(t, <-results)
	}

	require.Equal(t, uint64(1), db.Stats().Syncs)
}

func TestLogMetadataSurvivesBackupRestoreAndLargeHistory(t *testing.T) {
	db, path := openIdentityDatabase(t)
	entry := testLogEntry(t, 1, Command{Operations: []CommandOperation{{Kind: CommandDelete, Key: []byte("missing")}}})

	ids := make([]uuid.UUID, 520)
	for i := range ids {
		ids[i] = uuid.NewV4()
		entry.Index, entry.RequestID = uint64(i+1), ids[i]
		assertLogResult(t, db, entry, CommandOK)
	}

	archive := filepath.Join(t.TempDir(), "backup")
	require.NoError(t, db.Backup(archive))
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
	destination := filepath.Join(t.TempDir(), "restored.db")
	require.NoError(t, Restore(archive, destination))
	require.NoError(t, Validate(destination))
	restored, err := Open(destination)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, restored.Close()) })
	position, err := restored.AppliedPosition()
	require.NoError(t, err)
	require.Equal(t, uint64(520), position.Index)

	for _, id := range []uuid.UUID{ids[0], ids[255], ids[519]} {
		result, found, err := restored.AppliedRequest(id)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, CommandOK, result.Code)
	}

	entry.Index, entry.RequestID = 521, ids[0]
	assertLogResult(t, restored, entry, CommandOK)
	require.ErrorIs(t, restored.Put(nil, nil), ErrReplicatedWriteRequired)
}

func TestLogMetadataByteOwnership(t *testing.T) {
	db, _ := openIdentityDatabase(t)
	entry := testLogEntry(t, 1, commandFixture())
	want := bytes.Clone(entry.Command)
	assertLogResult(t, db, entry, CommandOK)
	require.Equal(t, want, entry.Command)
	position, err := db.AppliedPosition()
	require.NoError(t, err)
	position.GroupID[0] ^= 1
	again, err := db.AppliedPosition()
	require.NoError(t, err)
	require.Equal(t, entry.GroupID, again.GroupID)
}

func TestLogReplicasPersistIdenticalPositionsAndOutcomes(t *testing.T) {
	first, _ := openIdentityDatabase(t)
	second, _ := openIdentityDatabase(t)

	entry := testLogEntry(t, 1, commandFixture())
	for index := uint64(1); index <= 4; index++ {
		entry.Index = index
		if index == 3 {
			entry.RequestID = uuid.NewV4()
		}

		if index == 4 {
			entry.Command, entry.RequestID = nil, uuid.Nil()
		}

		left, err := first.ApplyLogEntry(entry)
		require.NoError(t, err)
		right, err := second.ApplyLogEntry(entry)
		require.NoError(t, err)
		require.Equal(t, left, right)
	}

	left, err := first.Scan(nil, nil, MaxScanEntries)
	require.NoError(t, err)
	right, err := second.Scan(nil, nil, MaxScanEntries)
	require.NoError(t, err)
	require.Equal(t, left, right)
}

func TestLogMetadataDoesNotConsumeUserMutationBudget(t *testing.T) {
	db, path := openIdentityDatabase(t)

	command := Command{}
	for i := range MaxBatchEntries {
		command.Operations = append(command.Operations, CommandOperation{Kind: CommandPut, Key: []byte{0xff, byte(i)}})
	}

	assertLogResult(t, db, testLogEntry(t, 1, command), CommandOK)
	require.Equal(t, uint64(MaxBatchEntries+3), db.Stats().Writes)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestApplyLogEntryWaitsForSyncBeforePublishingPosition(t *testing.T) {
	db, path := openIdentityDatabase(t)
	entry := testLogEntry(t, 1, commandFixture())
	entered, release := blockFirstFlush(t, db)
	result := make(chan error, 1)
	go func() { _, err := db.ApplyLogEntry(entry); result <- err }()
	<-entered
	select {
	case err := <-result:
		t.Fatalf("entry acknowledged before sync: %v", err)
	default:
	}

	if db.mu.TryRLock() {
		db.mu.RUnlock()
		t.Fatal("applied position could be read before checkpoint finished")
	}

	closed := make(chan error, 1)
	go func() { closed <- db.Close() }()
	release()
	require.NoError(t, waitWriteResult(t, result))
	require.NoError(t, waitWriteResult(t, closed))
	require.NoError(t, Validate(path))
	_, err := db.AppliedPosition()
	require.ErrorIs(t, err, ErrClosed)
	_, _, err = db.AppliedRequest(entry.RequestID)
	require.ErrorIs(t, err, ErrClosed)
}
