package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDatabase_StatsCountsOperationsAndReturnsIndependentSnapshot(t *testing.T) {
	db, path := openWriterDatabase(t)
	assert.Equal(t, Stats{}, db.Stats())
	require.NoError(t, db.PutBatch([]Entry{{Key: []byte("a"), Value: []byte("first")}, {Key: []byte("b"), Value: nil}}))
	_, found, err := db.Get([]byte("a"))
	require.NoError(t, err)
	assert.True(t, found)
	_, found, err = db.Get([]byte("missing"))
	require.NoError(t, err)
	assert.False(t, found)
	before := db.Stats()
	assert.Equal(t, Stats{Reads: 2, WriteRequests: 1, Writes: 2, WriteBatches: 1, PageReads: 3, PageWrites: 2, Syncs: 1}, before)
	require.NoError(t, db.Close())
	_, _, err = db.Get(nil)
	require.ErrorIs(t, err, ErrClosed)
	require.ErrorIs(t, db.Put(nil, nil), ErrClosed)
	after := db.Stats()
	assert.Equal(t, uint64(3), after.Reads)
	assert.Equal(t, uint64(1), after.ReadErrors)
	assert.Equal(t, uint64(2), after.WriteRequests)
	assert.Equal(t, uint64(1), after.WriteErrors)
	assert.Equal(t, uint64(2), before.Reads)
	assert.Equal(t, before.PageReads, after.PageReads)
	assert.Equal(t, before.PageWrites, after.PageWrites)
	reopened, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, reopened.Close()) })
	assert.Equal(t, Stats{}, reopened.Stats())
}
