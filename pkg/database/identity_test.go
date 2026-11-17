package database

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"uuid"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/pager"
)

func openIdentityDatabase(t testing.TB) (*Database, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "database.db")
	require.NoError(t, Create(path))
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db, path
}

func TestDatabaseIdentityPersistsAcrossSplitsAndReopen(t *testing.T) {
	db, path := openIdentityDatabase(t)
	id := db.ID()
	require.NotEqual(t, uuid.Nil(), id)
	require.NoError(t, db.PutBatch(crashEntries()))
	require.Equal(t, id, db.ID())
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
	db, err := Open(path)
	require.NoError(t, err)
	require.Equal(t, id, db.ID())
	require.NoError(t, db.Close())
}

func identityWALFixture(t *testing.T) (string, []byte, []byte) {
	t.Helper()
	db, path := openIdentityDatabase(t)
	require.NoError(t, db.Close())
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	wal, err := openWAL(path)
	require.NoError(t, err)
	require.NoError(t, wal.commit(map[uint64][]byte{0: contents[:4096], 1: traversalLeafPage(t, "recovered")}, 8192))
	require.NoError(t, wal.file.Close())
	encoded, err := os.ReadFile(path + ".wal")
	require.NoError(t, err)
	return path, contents, encoded
}

func TestWALRejectsAnotherDatabaseIdentity(t *testing.T) {
	_, _, log := identityWALFixture(t)
	db, path := openIdentityDatabase(t)
	require.NoError(t, db.Close())
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path+".wal", log, 0o600))
	db, err = Open(path)
	require.Nil(t, db)
	require.ErrorIs(t, err, ErrWALIdentityMismatch)
	assertFileContents(t, path, before)
	assertFileContents(t, path+".wal", log)
}

func TestWALRepairsHeaderWithIntactIdentity(t *testing.T) {
	path, contents, _ := identityWALFixture(t)
	clear(contents[:32])
	require.NoError(t, os.WriteFile(path, contents, 0o600))
	db, err := Open(path)
	require.NoError(t, err)
	_, found, err := db.Get([]byte("recovered"))
	require.NoError(t, err)
	require.True(t, found)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestWALRefusesDamagedIdentity(t *testing.T) {
	path, contents, log := identityWALFixture(t)
	contents[32] ^= 1
	require.NoError(t, os.WriteFile(path, contents, 0o600))
	db, err := Open(path)
	require.Nil(t, db)
	require.ErrorIs(t, err, ErrWALIdentityMismatch)
	assertFileContents(t, path, contents)
	assertFileContents(t, path+".wal", log)
}

func TestWALIdentityMustMatchLoggedHeader(t *testing.T) {
	path, contents, log := identityWALFixture(t)
	log[32] ^= 1
	digest := sha256.Sum256(log[:len(log)-walTrailerSize])
	copy(log[len(log)-sha256.Size:], digest[:])
	require.NoError(t, os.WriteFile(path+".wal", log, 0o600))
	db, err := Open(path)
	require.Nil(t, db)
	require.ErrorIs(t, err, ErrInvalidWAL)
	assertFileContents(t, path, contents)
}

func TestLegacyWALRecoveryAndIdentityPreservation(t *testing.T) {
	path, encoded := walFixture(t)
	legacy := append([]byte{}, encoded[:32]...)
	copy(legacy[:8], "GODBWAL1")
	legacy = append(legacy, encoded[walHeaderSize:]...)
	digest := sha256.Sum256(legacy[:len(legacy)-walTrailerSize])
	copy(legacy[len(legacy)-sha256.Size:], digest[:])
	require.NoError(t, os.WriteFile(path+".wal", legacy, 0o600))
	db, err := Open(path)
	require.NoError(t, err)
	require.Equal(t, uuid.Nil(), db.ID())
	require.NoError(t, db.PutBatch(crashEntries()))
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
	p, err := pager.OpenPager(path)
	require.NoError(t, err)
	header, err := inspect(p)
	require.NoError(t, err)
	require.Equal(t, uint16(1), header.FormatVersion)
	require.NoError(t, p.Close())

	other, otherPath := openIdentityDatabase(t)
	require.NoError(t, other.Close())
	require.NoError(t, os.WriteFile(otherPath+".wal", legacy, 0o600))
	_, err = Open(otherPath)
	require.ErrorIs(t, err, ErrWALIdentityMismatch)
}
