package database

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/pager"
)

func TestWALRecoveryCanBeRetriedAfterWriteFailure(t *testing.T) {
	path, _ := walFixture(t)
	wal, err := openWAL(path)
	require.NoError(t, err)
	contents := validDatabaseBytes()
	root := traversalLeafPage(t, "recovered")
	require.NoError(t, wal.commit(map[uint64][]byte{0: contents[:4096], 1: root}, 8192))
	p, err := pager.OpenPager(path)
	require.NoError(t, err)
	cause := errors.New("interrupted recovery")
	writes := 0
	hooks := &hookedStore{pageStore: p, write: func(id uint64, page []byte) error {
		writes++
		if writes == 2 {
			return cause
		}

		return p.WritePage(id, page)
	}}
	require.ErrorIs(t, wal.recover(hooks), cause)
	require.NoError(t, p.Close())
	require.NoError(t, wal.file.Close())
	require.ErrorIs(t, Validate(path), ErrRecoveryRequired)
	db, err := Open(path)
	require.NoError(t, err)
	value, found, err := db.Get([]byte("recovered"))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []byte("value:recovered"), value)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestWALDeleteAndCheckpointFailureRecovery(t *testing.T) {
	for _, checkpoint := range []bool{false, true} {
		db, path := openWriterDatabase(t)
		table, row := rowInsertFixture()
		require.NoError(t, db.CreateTable(table))
		require.NoError(t, db.InsertRow(table, row))
		cause := errors.New("delete checkpoint interrupted")

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
			db.pager = &hookedStore{pageStore: db.pager, sync: func() error { return cause }}
		}

		deleted, err := db.DeleteRow(table, row[1])
		require.False(t, deleted)
		require.ErrorIs(t, err, cause)
		require.NoError(t, db.Close())
		db, err = Open(path)
		require.NoError(t, err)
		_, found, err := db.GetRow(table, row[1])
		require.NoError(t, err)
		require.False(t, found)
		require.NoError(t, db.Close())
		require.NoError(t, Validate(path))
	}
}

func TestWALRejectsMissingAppendedPages(t *testing.T) {
	path, _ := walFixture(t)
	wal, err := openWAL(path)
	require.NoError(t, err)
	header, err := pager.DecodeHeader(validDatabaseBytes()[:4096])
	require.NoError(t, err)
	header.PageCount = 4
	page, err := pager.EncodeHeader(header)
	require.NoError(t, err)
	require.NoError(t, wal.commit(map[uint64][]byte{0: page, 1: validDatabaseBytes()[4096:]}, 4*4096))
	require.NoError(t, wal.file.Close())
	db, err := Open(path)
	require.ErrorIs(t, err, ErrInvalidWAL)
	require.Nil(t, db)
	assertFileContents(t, path, validDatabaseBytes())
}

func TestCreateRejectsExistingWAL(t *testing.T) {
	path := t.TempDir() + "/new.db"
	require.NoError(t, os.WriteFile(path+".wal", []byte("pending"), 0o600))
	require.ErrorIs(t, Create(path), ErrRecoveryRequired)
	_, err := os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestWALRepairsHeaderBeforeInspection(t *testing.T) {
	path, _ := walFixture(t)
	broken := validDatabaseBytes()
	clear(broken[:4096])
	require.NoError(t, os.WriteFile(path, broken, 0o600))
	db, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	assertFileContents(t, path, validDatabaseBytes())
	require.NoError(t, Validate(path))
}

func TestStagedPagesOwnBytesAndDoNotWrite(t *testing.T) {
	db, path := openWriterDatabase(t)
	stage, err := newStagedPages(db.pager)
	require.NoError(t, err)
	page := traversalLeafPage(t, "staged")
	require.NoError(t, stage.WritePage(1, page))
	page[0] = 0xff
	stored, err := stage.ReadPage(1)
	require.NoError(t, err)
	require.Equal(t, byte(pager.PageTypeLeaf), stored[0])
	stored[0] = 0xff
	stored, err = stage.ReadPage(1)
	require.NoError(t, err)
	require.Equal(t, byte(pager.PageTypeLeaf), stored[0])
	id, err := stage.AppendPage(stored)
	require.NoError(t, err)
	require.Equal(t, uint64(2), id)
	size, err := stage.GetFileSize()
	require.NoError(t, err)
	require.Equal(t, uint64(12288), size)
	assertFileContents(t, path, validDatabaseBytes())
}
