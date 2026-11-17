package database

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteTxRecoversProcessCrashes(t *testing.T) {
	testAtomicRecovery(t, "GODB_CRASH_TRANSACTION")
}

func TestApplyCommandRecoversProcessCrashes(t *testing.T) {
	testAtomicRecovery(t, "GODB_CRASH_COMMAND")
}

func testAtomicRecovery(t *testing.T, mode string) {
	t.Helper()

	for _, phase := range []string{"IncompleteWAL", "WALSynced", "PartialDatabase", "DatabaseSynced", "Checkpoint"} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "transaction.db")
			require.NoError(t, Create(path))
			cmd := exec.Command(os.Args[0], "-test.run=^TestWALCrashChild$")
			cmd.Env = append(os.Environ(), "GODB_CRASH_PATH="+path, "GODB_CRASH_PHASE="+phase, mode+"=1")
			output, err := cmd.CombinedOutput()

			var exitErr *exec.ExitError

			require.ErrorAs(t, err, &exitErr, "%s", output)
			require.Equal(t, 91, exitErr.ExitCode(), "%s", output)

			for range 2 {
				db, err := Open(path)
				require.NoError(t, err)
				table, _ := rowInsertFixture()
				_, found, err := db.GetTable(table.ID)
				require.NoError(t, err)
				require.Equal(t, phase != "IncompleteWAL", found)

				if found {
					assertStoredRow(t, db, table, numberedRow(2))
					_, found, err := db.GetRow(table, numberedRow(1)[1])
					require.NoError(t, err)
					require.False(t, found)
				}

				for _, entry := range crashEntries() {
					value, found, err := db.Get(entry.Key)
					require.NoError(t, err)
					require.Equal(t, phase != "IncompleteWAL", found)

					if found {
						require.Equal(t, entry.Value, value)
					}
				}

				require.NoError(t, db.Close())
				require.NoError(t, Validate(path))
			}
		})
	}
}

func TestWriteTxCommitFailureRequiresRecovery(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, row := rowInsertFixture()
	cause := errors.New("interrupted transaction checkpoint")
	store := db.pager
	writes := 0
	db.pager = &hookedStore{pageStore: store, write: func(id uint64, page []byte) error {
		writes++
		if writes == 2 {
			return cause
		}

		return store.WritePage(id, page)
	}}
	tx := db.BeginWrite()
	require.NoError(t, tx.CreateTable(table))
	require.NoError(t, tx.InsertRow(table, row))
	require.ErrorIs(t, tx.Commit(), cause)
	require.ErrorIs(t, tx.Commit(), ErrTransactionClosed)
	require.ErrorIs(t, tx.Rollback(), ErrTransactionClosed)
	require.ErrorIs(t, db.Put(nil, nil), ErrWriteFailed)
	require.Zero(t, db.Stats().Writes)
	require.NoError(t, db.Close())
	require.ErrorIs(t, Validate(path), ErrRecoveryRequired)
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	assertStoredRow(t, db, table, row)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}
