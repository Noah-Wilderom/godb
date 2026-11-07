package database

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDatabase_CloseErrorStillPreventsOperations(t *testing.T) {
	db, err := Open(writeDatabaseFixture(t, validDatabaseBytes()))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	require.NoError(t, db.pager.Close())
	require.ErrorIs(t, db.Close(), os.ErrClosed)

	value, found, err := db.Get([]byte("a"))
	require.ErrorIs(t, err, ErrClosed)
	assert.Nil(t, value)
	assert.False(t, found)
	require.ErrorIs(t, db.Put([]byte("a"), []byte("value")), ErrClosed)
	assert.NoError(t, db.Close())
}

func TestDatabase_CloseConcurrentWithOperations(t *testing.T) {
	db, err := Open(writeDatabaseFixture(t, databaseBytesWithEntries([]testEntry{record("a", "value")})))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	start := make(chan struct{})
	results := make(chan error, 48)

	for range 16 {
		go func() {
			<-start

			value, found, err := db.Get([]byte("a"))
			if err == nil && (!found || string(value) != "value") {
				err = errors.New("unexpected value before close")
			}

			if errors.Is(err, ErrClosed) {
				if value != nil || found {
					err = errors.New("closed read returned a value")
				} else {
					err = nil
				}
			}

			results <- err
		}()
		go func() {
			<-start

			err := db.Put([]byte("a"), []byte("value"))
			if errors.Is(err, ErrClosed) {
				err = nil
			}

			results <- err
		}()
		go func() {
			<-start
			results <- db.Close()
		}()
	}

	close(start)

	for range 48 {
		assert.NoError(t, <-results)
	}

	value, found, err := db.Get([]byte("a"))
	require.ErrorIs(t, err, ErrClosed)
	assert.Nil(t, value)
	assert.False(t, found)
	require.ErrorIs(t, db.Put([]byte("a"), nil), ErrClosed)
	assert.NoError(t, db.Close())
}
