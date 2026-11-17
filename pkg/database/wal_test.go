package database

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
)

type hookedJournal struct {
	journalFile

	write    func([]byte, int64) (int, error)
	sync     func() error
	truncate func(int64) error
}

func (f *hookedJournal) WriteAt(b []byte, offset int64) (int, error) {
	if f.write != nil {
		return f.write(b, offset)
	}

	return f.journalFile.WriteAt(b, offset)
}

func (f *hookedJournal) Sync() error {
	if f.sync != nil {
		return f.sync()
	}

	return f.journalFile.Sync()
}

func (f *hookedJournal) Truncate(size int64) error {
	if f.truncate != nil {
		return f.truncate(size)
	}

	return f.journalFile.Truncate(size)
}

func TestWALCrashChild(t *testing.T) {
	path := os.Getenv("GODB_CRASH_PATH")
	if path == "" {
		t.Skip("subprocess helper")
	}

	db, err := Open(path)
	require.NoError(t, err)
	phase := os.Getenv("GODB_CRASH_PHASE")
	journal := db.wal.file
	hooks := &hookedJournal{journalFile: journal}
	syncs, truncates := 0, 0
	hooks.sync = func() error {
		syncs++

		if err := journal.Sync(); err != nil {
			return err
		}

		if phase == "WALSynced" && syncs == 2 {
			os.Exit(91)
		}

		return nil
	}
	hooks.write = func(b []byte, offset int64) (int, error) {
		if phase == "IncompleteWAL" {
			_, err := journal.WriteAt(b[:len(b)/2], offset)
			require.NoError(t, err)
			require.NoError(t, journal.Sync())
			os.Exit(91)
		}

		return journal.WriteAt(b, offset)
	}
	hooks.truncate = func(size int64) error {
		truncates++

		if err := journal.Truncate(size); err != nil {
			return err
		}

		if phase == "Checkpoint" && truncates == 2 {
			os.Exit(91)
		}

		return nil
	}
	db.wal.file = hooks
	store := db.pager

	db.pager = &hookedStore{pageStore: store, write: func(id uint64, page []byte) error {
		if err := store.WritePage(id, page); err != nil {
			return err
		}

		if phase == "PartialDatabase" {
			os.Exit(91)
		}

		return nil
	}, sync: func() error {
		if err := store.Flush(); err != nil {
			return err
		}

		if phase == "DatabaseSynced" {
			os.Exit(91)
		}

		return nil
	}}

	if mode := os.Getenv("GODB_CRASH_LOG"); mode != "" {
		_, err := db.ApplyLogEntry(crashLogEntry(t, mode))
		require.NoError(t, err)
	} else if os.Getenv("GODB_CRASH_COMMAND") == "1" {
		table, _ := rowInsertFixture()
		key := numberedRow(1)[1]

		command := Command{Operations: []CommandOperation{
			{Kind: CommandCreateTable, Table: &table},
			{Kind: CommandInsertRow, Table: &table, Row: numberedRow(1)},
			{Kind: CommandInsertRow, Table: &table, Row: numberedRow(2)},
			{Kind: CommandDeleteRow, Table: &table, PrimaryKey: &key},
		}}
		for _, entry := range crashEntries() {
			command.Operations = append(command.Operations, CommandOperation{Kind: CommandPut, Key: entry.Key, Value: entry.Value})
		}

		encoded, err := EncodeCommand(command)
		require.NoError(t, err)
		_, err = db.ApplyCommand(encoded)
		require.NoError(t, err)
	} else if os.Getenv("GODB_CRASH_TRANSACTION") == "1" {
		tx := db.BeginWrite()
		table, _ := rowInsertFixture()
		require.NoError(t, tx.CreateTable(table))
		require.NoError(t, tx.InsertRow(table, numberedRow(1)))
		require.NoError(t, tx.InsertRow(table, numberedRow(2)))
		require.NoError(t, tx.DeleteRow(table, numberedRow(1)[1]))
		require.NoError(t, tx.PutBatch(crashEntries()))
		row, found, err := tx.GetRow(table, numberedRow(2)[1])
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, numberedRow(2), row)
		require.NoError(t, tx.Commit())
	} else {
		require.NoError(t, db.PutBatch(crashEntries()))
	}

	t.Fatal("crash hook did not fire")
}

func crashEntries() []Entry {
	entries := make([]Entry, 10)
	for i := range entries {
		entries[i] = Entry{Key: fmt.Appendf(nil, "key%02d", i), Value: bytes.Repeat([]byte("x"), 3000)}
	}

	return entries
}

func TestWALRecoversProcessCrashes(t *testing.T) {
	for _, phase := range []string{"IncompleteWAL", "WALSynced", "PartialDatabase", "DatabaseSynced", "Checkpoint"} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "crash.db")
			require.NoError(t, Create(path))
			cmd := exec.Command(os.Args[0], "-test.run=^TestWALCrashChild$")
			cmd.Env = append(os.Environ(), "GODB_CRASH_PATH="+path, "GODB_CRASH_PHASE="+phase)
			output, err := cmd.CombinedOutput()

			var exitErr *exec.ExitError

			require.ErrorAs(t, err, &exitErr, "%s", output)
			require.Equal(t, 91, exitErr.ExitCode(), "%s", output)

			if phase != "Checkpoint" {
				require.ErrorIs(t, Validate(path), ErrRecoveryRequired)
			}

			for range 2 {
				db, err := Open(path)
				require.NoError(t, err)

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

			info, err := os.Stat(path + ".wal")
			require.NoError(t, err)
			require.Zero(t, info.Size())
		})
	}
}

func TestWALFailedUpdateRecoversAllIndexes(t *testing.T) {
	db, path := openWriterDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	require.NoError(t, db.InsertRow(table, row))
	row[2] = recordpkg.NewBytes([]byte("replacement"))
	cause := errors.New("database page write failed")
	store := db.pager
	writes := 0
	db.pager = &hookedStore{pageStore: store, write: func(id uint64, page []byte) error {
		writes++
		if writes == 2 {
			return cause
		}

		return store.WritePage(id, page)
	}}
	matched, err := db.UpdateRow(table, row)
	require.False(t, matched)
	require.ErrorIs(t, err, cause)
	require.ErrorIs(t, err, ErrWriteFailed)
	require.NoError(t, db.Close())
	require.ErrorIs(t, Validate(path), ErrRecoveryRequired)
	db, err = Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	assertStoredRow(t, db, table, row)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestWALFailuresBeforeDatabaseWrites(t *testing.T) {
	for _, phase := range []string{"ShortWrite", "Sync", "Truncate"} {
		t.Run(phase, func(t *testing.T) {
			db, path := openWriterDatabase(t)
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			file := db.wal.file
			hooks := &hookedJournal{journalFile: file}
			cause := errors.New("WAL failure")
			syncs := 0

			switch phase {
			case "ShortWrite":
				hooks.write = func(b []byte, off int64) (int, error) { return file.WriteAt(b[:len(b)/2], off) }
				cause = io.ErrShortWrite
			case "Sync":
				hooks.sync = func() error {
					syncs++
					if syncs == 2 {
						return cause
					}

					return file.Sync()
				}
			case "Truncate":
				hooks.truncate = func(int64) error { return cause }
			}

			db.wal.file = hooks
			err = db.PutBatch(crashEntries())
			require.ErrorIs(t, err, cause)
			require.ErrorIs(t, err, ErrWriteFailed)
			require.Zero(t, db.Stats().PageWrites)
			assertFileContents(t, path, before)
			require.NoError(t, db.Close())
			db, err = Open(path)
			require.NoError(t, err)
			_, found, err := db.Get(crashEntries()[0].Key)
			require.NoError(t, err)
			require.Equal(t, phase == "Sync", found)
			require.NoError(t, db.Close())
		})
	}
}

func walFixture(t testing.TB) (string, []byte) {
	t.Helper()
	path := writeDatabaseFixture(t, validDatabaseBytes())
	wal, err := openWAL(path)
	require.NoError(t, err)
	contents := validDatabaseBytes()
	require.NoError(t, wal.commit(map[uint64][]byte{0: contents[:4096], 1: contents[4096:]}, 8192))
	require.NoError(t, wal.file.Close())
	encoded, err := os.ReadFile(path + ".wal")
	require.NoError(t, err)
	return path, encoded
}

func TestWALRejectsCorruptionWithoutWriting(t *testing.T) {
	for name, mutate := range map[string]func([]byte) []byte{
		"magic":          func(b []byte) []byte { b[0] ^= 1; return b },
		"checksum":       func(b []byte) []byte { b[100] ^= 1; return b },
		"trailer":        func(b []byte) []byte { b[len(b)-walTrailerSize] ^= 1; return b },
		"count limit":    func(b []byte) []byte { binary.LittleEndian.PutUint64(b[16:24], maxWALPages+1); return b },
		"bad size":       func(b []byte) []byte { binary.LittleEndian.PutUint64(b[8:16], 8193); return b },
		"trailing bytes": func(b []byte) []byte { return append(b, 0) },
		"duplicate ID": func(b []byte) []byte {
			binary.LittleEndian.PutUint64(b[walHeaderSize+walRecordSize:], 0)
			digest := sha256.Sum256(b[:len(b)-walTrailerSize])
			copy(b[len(b)-sha256.Size:], digest[:])
			return b
		},
	} {
		t.Run(name, func(t *testing.T) {
			path, encoded := walFixture(t)
			bad := mutate(encoded)
			require.NoError(t, os.WriteFile(path+".wal", bad, 0o600))
			db, err := Open(path)
			require.Nil(t, db)
			require.ErrorIs(t, err, ErrInvalidWAL)
			assertFileContents(t, path, validDatabaseBytes())
			assertFileContents(t, path+".wal", bad)
		})
	}
}

func TestWALIncompleteRecordsDiscarded(t *testing.T) {
	for _, length := range []int{1, 31, 32, 100, walHeaderSize + walRecordSize, walHeaderSize + 2*walRecordSize + walTrailerSize - 1} {
		path, encoded := walFixture(t)
		require.NoError(t, os.WriteFile(path+".wal", encoded[:length], 0o600))
		db, err := Open(path)
		require.NoError(t, err)
		require.NoError(t, db.Close())
		assertFileContents(t, path, validDatabaseBytes())
		assertFileContents(t, path+".wal", []byte{})
	}
}

func TestDatabase_ExclusiveOpenAndSymlink(t *testing.T) {
	db, path := openWriterDatabase(t)
	second, err := Open(path)
	require.ErrorIs(t, err, ErrDatabaseLocked)
	require.Nil(t, second)
	link := filepath.Join(t.TempDir(), "alias.db")
	require.NoError(t, os.Symlink(path, link))
	second, err = Open(link)
	require.ErrorIs(t, err, ErrDatabaseLocked)
	require.Nil(t, second)
	require.NoError(t, db.Close())
	second, err = Open(link)
	require.NoError(t, err)
	require.NoError(t, second.Close())
	_, err = os.Stat(link + ".wal")
	require.ErrorIs(t, err, os.ErrNotExist)
}
