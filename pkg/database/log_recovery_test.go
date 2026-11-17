package database

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"uuid"

	"github.com/stretchr/testify/require"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func crashLogEntry(t *testing.T, mode string) LogEntry {
	t.Helper()

	entry := LogEntry{GroupID: uuid.MustParse("11111111-1111-4111-8111-111111111111"), Index: 1, Term: 1}
	if mode == "Noop" {
		return entry
	}

	command := Command{}
	if mode == "Success" || mode == "Delete" {
		command = commandFixture()
		table, _ := rowInsertFixture()

		key := recordpkg.NewUint64(1)
		if mode == "Delete" {
			command.Operations = append(command.Operations, CommandOperation{Kind: CommandDeleteRowCounted, Table: &table, PrimaryKey: &key})
		} else {
			command.Operations = append(command.Operations, CommandOperation{Kind: CommandPatchRow, Table: &table, PrimaryKey: &key, Row: schema.Row{3: recordpkg.NewInt64(99)}})
		}
	}

	for _, data := range crashEntries() {
		command.Operations = append(command.Operations, CommandOperation{Kind: CommandPut, Key: data.Key, Value: data.Value})
	}

	if mode == "Reject" {
		table, row := rowInsertFixture()
		command.Operations = append(command.Operations, CommandOperation{Kind: CommandInsertRow, Table: &table, Row: row})
	}

	encoded, err := EncodeCommand(command)
	require.NoError(t, err)
	entry.Command, entry.RequestID = encoded, uuid.MustParse("22222222-2222-4222-8222-222222222222")
	return entry
}

func TestApplyLogEntryRecoversPositionOutcomeAndDataTogether(t *testing.T) {
	for _, mode := range []string{"Success", "Delete", "Reject", "Noop"} {
		for _, phase := range []string{"IncompleteWAL", "WALSynced", "PartialDatabase", "DatabaseSynced", "Checkpoint"} {
			t.Run(mode+"/"+phase, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "crash.db")
				require.NoError(t, Create(path))
				cmd := exec.Command(os.Args[0], "-test.run=^TestWALCrashChild$")
				cmd.Env = append(os.Environ(), "GODB_CRASH_PATH="+path, "GODB_CRASH_PHASE="+phase, "GODB_CRASH_LOG="+mode)
				output, err := cmd.CombinedOutput()

				var exitErr *exec.ExitError

				require.ErrorAs(t, err, &exitErr, "%s", output)
				require.Equal(t, 91, exitErr.ExitCode(), "%s", output)
				entry := crashLogEntry(t, mode)

				code := CommandOK
				if mode == "Reject" {
					code = CommandTableNotFound
				}

				for range 2 {
					db, err := Open(path)
					require.NoError(t, err)
					position, err := db.AppliedPosition()
					require.NoError(t, err)

					if phase == "IncompleteWAL" {
						require.Zero(t, position.Index)
					} else {
						require.Equal(t, LogPosition{GroupID: entry.GroupID, Index: 1, Term: 1}, position)
					}

					if mode != "Noop" {
						result, found, err := db.AppliedRequest(entry.RequestID)
						require.NoError(t, err)
						require.Equal(t, phase != "IncompleteWAL", found)

						if found {
							require.Equal(t, code, result.Code)

							if mode == "Success" || mode == "Delete" {
								require.Equal(t, uint64(1), result.AffectedRows)
								require.Equal(t, uint64(1), result.MatchedRows)
							}
						}
					}

					for _, data := range crashEntries() {
						value, found, err := db.Get(data.Key)
						require.NoError(t, err)
						require.Equal(t, phase != "IncompleteWAL" && (mode == "Success" || mode == "Delete"), found)

						if found {
							require.Equal(t, data.Value, value)
						}
					}

					if mode == "Delete" && phase != "IncompleteWAL" {
						table, _ := rowInsertFixture()
						_, found, err := db.GetRow(table, recordpkg.NewUint64(1))
						require.NoError(t, err)
						require.False(t, found)

						for _, index := range table.Indexes {
							rows, err := db.ScanRowsByIndex(table, index.ID, 10)
							require.NoError(t, err)
							require.Empty(t, rows)
						}
					}

					if phase != "IncompleteWAL" {
						before := db.Stats()
						assertLogResult(t, db, entry, code)
						require.Equal(t, before.PageWrites, db.Stats().PageWrites)
					}

					require.NoError(t, db.Close())
					require.NoError(t, Validate(path))
				}
			})
		}
	}
}

func TestApplyLogEntryStorageFailureRecoversSavedResult(t *testing.T) {
	for _, checkpoint := range []bool{false, true} {
		db, path := openIdentityDatabase(t)
		cause := errors.New("log apply interrupted")

		if checkpoint {
			file := db.wal.file
			truncates := 0
			db.wal.file = &hookedJournal{journalFile: file, truncate: func(size int64) error {
				truncates++
				if truncates == 2 {
					return cause
				}

				return file.Truncate(size)
			}}
		} else {
			store := db.pager
			writes := 0
			db.pager = &hookedStore{pageStore: store, write: func(id uint64, page []byte) error {
				writes++
				if writes == 2 {
					return cause
				}

				return store.WritePage(id, page)
			}}
		}

		entry := testLogEntry(t, 1, commandFixture())
		result, err := db.ApplyLogEntry(entry)
		require.Empty(t, result.Code)
		require.ErrorIs(t, err, cause)
		require.ErrorIs(t, err, ErrWriteFailed)
		_, err = db.AppliedPosition()
		require.ErrorIs(t, err, ErrWriteFailed)
		require.NoError(t, db.Close())
		db, err = Open(path)
		require.NoError(t, err)
		position, err := db.AppliedPosition()
		require.NoError(t, err)
		require.Equal(t, uint64(1), position.Index)
		assertLogResult(t, db, entry, CommandOK)
		require.Zero(t, db.Stats().PageWrites)
		require.NoError(t, db.Close())
		require.NoError(t, Validate(path))
	}
}
