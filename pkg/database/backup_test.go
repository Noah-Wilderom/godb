package database

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"uuid"

	"github.com/stretchr/testify/require"
)

func TestBackupRestoreCatalogRowsAndFreshIdentity(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		var db *Database

		if legacy {
			db, _ = openWriterDatabase(t)
		} else {
			db, _ = openIdentityDatabase(t)
		}

		table, _ := rowInsertFixture()
		tx := db.BeginWrite()
		require.NoError(t, tx.CreateTable(table))

		for id := uint64(1); id <= 10; id++ {
			require.NoError(t, tx.InsertRow(table, numberedRow(id)))
		}

		require.NoError(t, tx.PutBatch(crashEntries()))
		require.NoError(t, tx.Commit())
		archive := filepath.Join(t.TempDir(), "snapshot.godb-backup")
		before := db.Stats()
		require.NoError(t, db.Backup(archive))
		require.Equal(t, before.PageWrites, db.Stats().PageWrites)
		require.Equal(t, before.Syncs, db.Stats().Syncs)
		backupInfo, err := os.Stat(archive)
		require.NoError(t, err)
		require.Zero(t, backupInfo.Mode().Perm()&0o077)
		require.NoError(t, db.Put([]byte("after-backup"), nil))

		var previousID uuid.UUID

		for range 2 {
			destination := filepath.Join(t.TempDir(), "restored.db")
			require.NoError(t, Restore(archive, destination))
			require.NoError(t, Validate(destination))
			restored, err := Open(destination)
			require.NoError(t, err)
			require.NotEqual(t, uuid.Nil(), restored.ID())
			require.NotEqual(t, db.ID(), restored.ID())
			require.NotEqual(t, previousID, restored.ID())

			previousID = restored.ID()
			for id := uint64(1); id <= 10; id++ {
				assertStoredRow(t, restored, table, numberedRow(id))
			}

			_, found, err := restored.Get([]byte("after-backup"))
			require.NoError(t, err)
			require.False(t, found)
			require.NoError(t, restored.Put([]byte("new-write"), nil))
			require.NoError(t, restored.Close())
			require.NoError(t, Validate(destination))
		}
	}
}

func backupFixture(t testing.TB) (string, []byte) {
	t.Helper()
	db, _ := openIdentityDatabase(t)
	path := filepath.Join(t.TempDir(), "backup")
	require.NoError(t, db.Backup(path))
	encoded, err := os.ReadFile(path)
	require.NoError(t, err)
	return path, encoded
}

func TestRestoreRejectsCorruptionWithoutPublishing(t *testing.T) {
	_, original := backupFixture(t)
	for name, mutate := range map[string]func([]byte) []byte{
		"Magic":                   func(b []byte) []byte { b[0] ^= 1; return b },
		"TruncatedHeader":         func(b []byte) []byte { return b[:10] },
		"TruncatedPayload":        func(b []byte) []byte { return b[:len(b)-1] },
		"TrailingByte":            func(b []byte) []byte { return append(b, 0) },
		"SizeOverflow":            func(b []byte) []byte { binary.LittleEndian.PutUint64(b[8:16], ^uint64(0)); return b },
		"Payload":                 func(b []byte) []byte { b[backupHeaderSize+4096+100] ^= 1; return b },
		"Digest":                  func(b []byte) []byte { b[len(b)-1] ^= 1; return b },
		"IdentityWithValidDigest": func(b []byte) []byte { b[16] ^= 1; return rehashBackup(b) },
		"TreeWithValidDigest":     func(b []byte) []byte { b[backupHeaderSize+4096] = 255; return rehashBackup(b) },
		"HeaderWithValidDigest":   func(b []byte) []byte { b[backupHeaderSize] = 0; return rehashBackup(b) },
	} {
		t.Run(name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "bad.backup")
			bad := mutate(bytes.Clone(original))
			require.NoError(t, os.WriteFile(archive, bad, 0o600))
			dir := t.TempDir()
			destination := filepath.Join(dir, "restored.db")
			require.ErrorIs(t, Restore(archive, destination), ErrInvalidBackup)
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			require.Empty(t, entries)
			assertFileContents(t, archive, bad)
		})
	}
}

func rehashBackup(encoded []byte) []byte {
	digest := sha256.Sum256(encoded[:len(encoded)-sha256.Size])
	copy(encoded[len(encoded)-sha256.Size:], digest[:])
	return encoded
}

func TestBackupRestoreNeverOverwrite(t *testing.T) {
	archive, _ := backupFixture(t)
	db, path := openIdentityDatabase(t)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	require.ErrorIs(t, db.Backup(path), os.ErrExist)
	require.ErrorIs(t, Restore(archive, path), os.ErrExist)
	assertFileContents(t, path, before)

	for _, mode := range []string{"File", "Symlink", "WAL", "DanglingWAL"} {
		t.Run(mode, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "restored.db")

			var existing string

			switch mode {
			case "File":
				existing = destination
				require.NoError(t, os.WriteFile(existing, []byte("preserve"), 0o600))
			case "Symlink":
				require.NoError(t, os.Symlink(path, destination))
			case "WAL":
				existing = destination + ".wal"
				require.NoError(t, os.WriteFile(existing, []byte("preserve"), 0o600))
			case "DanglingWAL":
				require.NoError(t, os.Symlink(destination+".missing", destination+".wal"))
			}

			require.ErrorIs(t, Restore(archive, destination), os.ErrExist)

			if existing != "" {
				assertFileContents(t, existing, []byte("preserve"))
			}
		})
	}
}

func TestBackupHoldsConsistentViewWhileWriterWaits(t *testing.T) {
	db, _ := openIdentityDatabase(t)
	require.NoError(t, db.Put([]byte("key"), []byte("old")))
	store := db.pager
	entered, release := make(chan struct{}), make(chan struct{})

	var once sync.Once

	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	rootReads := 0
	db.pager = &hookedStore{pageStore: store, read: func(id uint64) ([]byte, error) {
		if id == 0 {
			rootReads++
			if rootReads == 2 {
				close(entered)
				<-release
			}
		}

		return store.ReadPage(id)
	}}
	archive := filepath.Join(t.TempDir(), "backup")
	backupResult := make(chan error, 1)
	go func() { backupResult <- db.Backup(archive) }()
	<-entered

	if db.mu.TryLock() {
		db.mu.Unlock()
		t.Fatal("backup did not hold the database read lock")
	}

	writeResult := make(chan error, 1)
	go func() { writeResult <- db.Put([]byte("key"), []byte("new")) }()
	unblock()
	require.NoError(t, waitWriteResult(t, backupResult))
	require.NoError(t, waitWriteResult(t, writeResult))
	destination := filepath.Join(t.TempDir(), "restored.db")
	require.NoError(t, Restore(archive, destination))
	restored, err := Open(destination)
	require.NoError(t, err)
	value, found, err := restored.Get([]byte("key"))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []byte("old"), value)
	require.NoError(t, restored.Close())
	value, _, err = db.Get([]byte("key"))
	require.NoError(t, err)
	require.Equal(t, []byte("new"), value)
}

func TestBackupCopyFailureCleansTemporaryFile(t *testing.T) {
	db, path := openIdentityDatabase(t)
	store := db.pager
	cause := errors.New("backup read interrupted")
	rootReads := 0
	db.pager = &hookedStore{pageStore: store, read: func(id uint64) ([]byte, error) {
		if id == 0 {
			rootReads++
			if rootReads == 2 {
				return nil, cause
			}
		}

		return store.ReadPage(id)
	}}
	dir := t.TempDir()
	require.ErrorIs(t, db.Backup(filepath.Join(dir, "backup")), cause)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries)
	require.NoError(t, db.Put([]byte("still-usable"), nil))
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestBackupRejectsInvalidDataAndFailedHandle(t *testing.T) {
	for _, mode := range []string{"CorruptIndex", "Closed", "Failed"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := openIdentityDatabase(t)
			want := ErrClosed

			switch mode {
			case "CorruptIndex":
				table, row := rowInsertFixture()
				require.NoError(t, db.CreateTable(table))
				require.NoError(t, db.InsertRow(table, row))
				plan, err := PlanRowInsert(table, row)
				require.NoError(t, err)
				_, err = db.Delete(plan.Indexes[0].Key)
				require.NoError(t, err)
				want = ErrInvalidData
			case "Closed":
				require.NoError(t, db.Close())
			case "Failed":
				db.pager = &hookedStore{pageStore: db.pager, sync: func() error { return errors.New("sync failed") }}
				require.ErrorIs(t, db.Put(nil, nil), ErrWriteFailed)
				want = ErrWriteFailed
			}

			archive := filepath.Join(t.TempDir(), "backup")
			require.ErrorIs(t, db.Backup(archive), want)
			_, err := os.Stat(archive)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestPublishNewFileFailureAndDestinationRace(t *testing.T) {
	for _, race := range []bool{false, true} {
		dir := t.TempDir()
		path := filepath.Join(dir, "output")
		cause := errors.New("copy failed")

		err := publishNewFile(path, func(file *os.File) error {
			_, err := file.Write([]byte("temporary"))
			require.NoError(t, err)

			if race {
				return os.WriteFile(path, []byte("competing file"), 0o600)
			}

			return cause
		})
		if race {
			require.ErrorIs(t, err, os.ErrExist)
			assertFileContents(t, path, []byte("competing file"))
		} else {
			require.ErrorIs(t, err, cause)
		}

		matches, err := filepath.Glob(filepath.Join(dir, ".godb-*"))
		require.NoError(t, err)
		require.Empty(t, matches)
	}
}

func TestRestoreRejectsSourceWALOnRestoredIdentity(t *testing.T) {
	path, _, log := identityWALFixture(t)
	db, err := Open(path)
	require.NoError(t, err)
	archive := filepath.Join(t.TempDir(), "backup")
	require.NoError(t, db.Backup(archive))
	require.NoError(t, db.Close())
	destination := filepath.Join(t.TempDir(), "restored.db")
	require.NoError(t, Restore(archive, destination))
	require.NoError(t, os.WriteFile(destination+".wal", log, 0o600))
	_, err = Open(destination)
	require.ErrorIs(t, err, ErrWALIdentityMismatch)
}
