package database

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/pager"
	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
)

func TestDatabase_ValuesPersistAndReplaceAcrossReopen(t *testing.T) {
	db, path := openWriterDatabase(t)
	decimal, err := recordpkg.NewDecimal("123.4500")
	require.NoError(t, err)

	values := []recordpkg.Value{
		recordpkg.NewInt64(-42),
		recordpkg.NewNull(),
		recordpkg.NewMap(map[string]recordpkg.Value{
			"amount": decimal,
			"items": recordpkg.NewArray([]recordpkg.Value{
				recordpkg.NewNull(), recordpkg.NewBool(true), recordpkg.NewBytes([]byte{0, 0xff}),
			}),
		}),
	}
	for i, value := range values {
		require.NoError(t, db.PutValue([]byte{byte(i)}, value))
	}

	values[0] = recordpkg.NewBool(false)
	require.NoError(t, db.PutValue([]byte{0}, values[0]))
	require.NoError(t, db.Close())
	reopened, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, reopened.Close()) })

	for i, expected := range values {
		key := []byte{byte(i)}
		value, found, err := reopened.GetValue(key)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, expected.Type(), value.Type())
		want, err := recordpkg.EncodeValue(expected)
		require.NoError(t, err)
		got, err := recordpkg.EncodeValue(value)
		require.NoError(t, err)
		assert.Equal(t, want, got)
		raw, found, err := reopened.Get(key)
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, want, raw)
	}
}

func TestDatabase_GetValueDistinguishesMissingAndNull(t *testing.T) {
	db, _ := openWriterDatabase(t)
	key := []byte("key")
	value, found, err := db.GetValue(key)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, recordpkg.Value{}, value)
	require.NoError(t, db.PutValue(key, recordpkg.NewNull()))
	value, found, err = db.GetValue(key)
	require.NoError(t, err)
	assert.True(t, found)
	assert.True(t, value.IsNull())
}

func TestDatabase_GetValueRejectsMalformedStoredBytes(t *testing.T) {
	for _, payload := range [][]byte{nil, {0xff}, {byte(recordpkg.TypeBool)}, {byte(recordpkg.TypeBool), 2}, {byte(recordpkg.TypeNull), 0}} {
		db, _ := openWriterDatabase(t)
		key := []byte("key")
		require.NoError(t, db.Put(key, payload))
		value, found, err := db.GetValue(key)
		require.ErrorContains(t, err, "decode value")
		assert.False(t, found)
		assert.Equal(t, recordpkg.Value{}, value)
		raw, found, err := db.Get(key)
		require.NoError(t, err)
		assert.True(t, found)
		assert.True(t, bytes.Equal(payload, raw))
		require.NoError(t, db.PutValue(key, recordpkg.NewBool(true)))
		_, found, err = db.GetValue(key)
		require.NoError(t, err)
		assert.True(t, found)
	}
}

func TestDatabase_PutValueEncodingFailurePreservesExistingValue(t *testing.T) {
	db, path := openWriterDatabase(t)
	key := []byte("key")
	require.NoError(t, db.PutValue(key, recordpkg.NewInt64(42)))
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	stats := db.Stats()
	invalid := recordpkg.NewMap(map[string]recordpkg.Value{"\xff": recordpkg.NewNull()})
	require.ErrorContains(t, db.PutValue(key, invalid), "encode value")
	assert.Equal(t, stats, db.Stats())
	assertFileContents(t, path, before)
	value, found, err := db.GetValue(key)
	require.NoError(t, err)
	assert.True(t, found)
	got, err := value.Int64()
	require.NoError(t, err)
	assert.Equal(t, int64(42), got)
}

func TestDatabase_PutValueEnforcesEncodedEntrySize(t *testing.T) {
	db, path := openWriterDatabase(t)
	key := []byte("k")
	payload := bytes.Repeat([]byte{'a'}, 4083)
	require.NoError(t, db.PutValue(key, recordpkg.NewBytes(payload)))
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	require.ErrorIs(t, db.PutValue(key, recordpkg.NewBytes(append(bytes.Clone(payload), 'b'))), pager.ErrPagerEntryTooLarge)
	require.ErrorIs(t, db.PutValue(make([]byte, 4076), recordpkg.NewNull()), pager.ErrPagerEntryTooLarge)
	assertFileContents(t, path, before)
	value, found, err := db.GetValue(key)
	require.NoError(t, err)
	assert.True(t, found)
	got, err := value.Bytes()
	require.NoError(t, err)
	assert.Equal(t, payload, got)
}

func TestDatabase_ValueMethodsPropagateClosedError(t *testing.T) {
	db, _ := openWriterDatabase(t)
	require.NoError(t, db.Close())
	require.ErrorIs(t, db.PutValue(nil, recordpkg.NewNull()), ErrClosed)
	value, found, err := db.GetValue(nil)
	require.ErrorIs(t, err, ErrClosed)
	assert.False(t, found)
	assert.Equal(t, recordpkg.Value{}, value)
}

func TestDatabase_ValueMethodsPropagateStorageFailure(t *testing.T) {
	db, _ := openWriterDatabase(t)
	cause := errors.New("sync failed")
	db.pager = &hookedStore{pageStore: db.pager, sync: func() error { return cause }}
	err := db.PutValue([]byte("key"), recordpkg.NewBool(true))
	require.ErrorIs(t, err, cause)
	require.ErrorIs(t, err, ErrWriteFailed)
	value, found, err := db.GetValue([]byte("key"))
	require.ErrorIs(t, err, cause)
	require.ErrorIs(t, err, ErrWriteFailed)
	assert.False(t, found)
	assert.Equal(t, recordpkg.Value{}, value)
}
