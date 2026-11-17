package database

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func TestApplyCommandReplaysSameLogicalState(t *testing.T) {
	first, firstPath := openIdentityDatabase(t)
	second, secondPath := openIdentityDatabase(t)
	require.NotEqual(t, first.ID(), second.ID())

	entries := crashEntries()
	for _, entry := range entries {
		require.NoError(t, first.Put(entry.Key, entry.Value))
	}

	slices.Reverse(entries)

	for _, entry := range entries {
		require.NoError(t, second.Put(entry.Key, entry.Value))
	}

	encoded, err := EncodeCommand(commandFixture())
	require.NoError(t, err)
	original := bytes.Clone(encoded)

	for _, db := range []*Database{first, second} {
		before := db.Stats()
		result, err := db.ApplyCommand(encoded)
		require.NoError(t, err)
		require.Equal(t, CommandOK, result.Code)
		require.Equal(t, before.Syncs+1, db.Stats().Syncs)
		require.Equal(t, before.WriteRequests+1, db.Stats().WriteRequests)
		table, _ := rowInsertFixture()
		row := numberedRow(1)
		row[2] = recordpkg.NewBytes([]byte("changed"))
		assertStoredRow(t, db, table, row)
		_, found, err := db.GetRow(table, numberedRow(2)[1])
		require.NoError(t, err)
		require.False(t, found)
		_, found, err = db.Get([]byte("raw"))
		require.NoError(t, err)
		require.False(t, found)
		_, found, err = db.Get(nil)
		require.NoError(t, err)
		require.True(t, found)
	}

	left, err := first.Scan(nil, nil, MaxScanEntries)
	require.NoError(t, err)
	right, err := second.Scan(nil, nil, MaxScanEntries)
	require.NoError(t, err)
	require.Equal(t, left, right)
	require.Equal(t, original, encoded)

	for _, db := range []*Database{first, second} {
		result, err := db.ApplyCommand(encoded)
		require.NoError(t, err)
		require.Equal(t, CommandTableIDExists, result.Code)
	}

	require.NoError(t, first.Close())
	require.NoError(t, second.Close())

	for _, path := range []string{firstPath, secondPath} {
		require.NoError(t, Validate(path))
		db, err := Open(path)
		require.NoError(t, err)
		got, err := db.Scan(nil, nil, MaxScanEntries)
		require.NoError(t, err)
		require.Equal(t, left, got)
		require.NoError(t, db.Close())
	}
}

func TestApplyCommandStableRejectionsRollBackEarlierOperations(t *testing.T) {
	for _, code := range []CommandResultCode{CommandDuplicatePrimaryKey, CommandUniqueConflict, CommandTableIDExists, CommandTableNameExists, CommandTableNotFound, CommandSchemaMismatch} {
		t.Run(string(code), func(t *testing.T) {
			db, path := openIdentityDatabase(t)

			table, row := rowInsertFixture()
			if code != CommandTableNotFound {
				require.NoError(t, db.CreateTable(table))
				require.NoError(t, db.InsertRow(table, row))
			}

			op := CommandOperation{Kind: CommandInsertRow, Table: &table, Row: row}

			switch code {
			case CommandUniqueConflict:
				op.Row = numberedRow(99)
				op.Row[2] = row[2]
			case CommandTableIDExists:
				op = CommandOperation{Kind: CommandCreateTable, Table: &table}
			case CommandTableNameExists:
				table.ID = 2
				op = CommandOperation{Kind: CommandCreateTable, Table: &table}
			case CommandSchemaMismatch:
				table.Name = "different"
			}

			command := Command{}
			for _, entry := range crashEntries() {
				command.Operations = append(command.Operations, CommandOperation{Kind: CommandPut, Key: entry.Key, Value: entry.Value})
			}

			command.Operations = append(command.Operations, op)
			encoded, err := EncodeCommand(command)
			require.NoError(t, err)
			contents, err := os.ReadFile(path)
			require.NoError(t, err)
			header := *db.header
			before := db.Stats()
			result, err := db.ApplyCommand(encoded)
			require.NoError(t, err)
			require.Equal(t, code, result.Code)
			require.Equal(t, header, *db.header)
			require.Equal(t, before.PageWrites, db.Stats().PageWrites)
			require.Equal(t, before.Syncs, db.Stats().Syncs)
			assertFileContents(t, path, contents)
			require.NoError(t, db.Put([]byte("still-usable"), nil))
			require.NoError(t, db.Close())
			require.NoError(t, Validate(path))
		})
	}
}

func TestApplyCommandMutationLimitAndNoopResults(t *testing.T) {
	db, _ := openIdentityDatabase(t)

	table := schema.Table{ID: 1, Name: "t", PrimaryKeyColumnID: 1, Columns: []schema.Column{
		{ID: 1, Name: "id", Type: recordpkg.TypeUint64}, {ID: 2, Name: "v", Type: recordpkg.TypeUint64},
	}}
	for id := uint32(1); id <= 32; id++ {
		table.Indexes = append(table.Indexes, schema.Index{ID: id, Name: fmt.Sprint(id), ColumnIDs: []uint32{2}})
	}

	require.NoError(t, db.CreateTable(table))
	row := schema.Row{1: recordpkg.NewUint64(1), 2: recordpkg.NewUint64(1)}
	require.NoError(t, db.InsertRow(table, row))
	before := db.Stats()

	for _, mode := range []string{"Noop", "Missing", "TooManyChanges"} {
		row[1], row[2] = recordpkg.NewUint64(1), recordpkg.NewUint64(1)

		switch mode {
		case "Missing":
			row[1] = recordpkg.NewUint64(99)
		case "TooManyChanges":
			row[2] = recordpkg.NewUint64(2)
		}

		encoded, err := EncodeCommand(Command{Operations: []CommandOperation{{Kind: CommandUpdateRow, Table: &table, Row: row}}})
		require.NoError(t, err)
		result, err := db.ApplyCommand(encoded)
		require.NoError(t, err)

		if mode == "TooManyChanges" {
			require.Equal(t, CommandMutationLimit, result.Code)
		} else {
			require.Equal(t, CommandOK, result.Code)
		}

		require.Equal(t, before.PageWrites, db.Stats().PageWrites)
	}
}

func TestApplyCommandMalformedAndClosed(t *testing.T) {
	db, _ := openIdentityDatabase(t)
	before := db.Stats()
	result, err := db.ApplyCommand([]byte("bad"))
	require.ErrorIs(t, err, ErrInvalidCommand)
	require.Empty(t, result.Code)
	require.Equal(t, before.WriteRequests, db.Stats().WriteRequests)
	encoded, err := EncodeCommand(commandFixture())
	require.NoError(t, err)
	require.NoError(t, db.Close())
	result, err = db.ApplyCommand(encoded)
	require.ErrorIs(t, err, ErrClosed)
	require.Empty(t, result.Code)
}

func TestApplyCommandStorageFailureIsNotARejection(t *testing.T) {
	db, path := openIdentityDatabase(t)
	store := db.pager
	writes := 0
	db.pager = &hookedStore{pageStore: store, write: func(id uint64, page []byte) error {
		writes++
		if writes == 2 {
			return ErrUniqueConflict
		}

		return store.WritePage(id, page)
	}}
	encoded, err := EncodeCommand(commandFixture())
	require.NoError(t, err)
	result, err := db.ApplyCommand(encoded)
	require.ErrorIs(t, err, ErrWriteFailed)
	require.ErrorIs(t, err, ErrUniqueConflict)
	require.Empty(t, result.Code)
	require.NoError(t, db.Close())
	db, err = Open(path)
	require.NoError(t, err)
	table, _ := rowInsertFixture()
	row := numberedRow(1)
	row[2] = recordpkg.NewBytes([]byte("changed"))
	assertStoredRow(t, db, table, row)
	require.NoError(t, db.Close())
	require.NoError(t, Validate(path))
}

func TestApplyCommandCorruptionIsAnError(t *testing.T) {
	db, _ := openIdentityDatabase(t)
	table, row := rowInsertFixture()
	require.NoError(t, db.CreateTable(table))
	require.NoError(t, db.InsertRow(table, row))
	plan, err := PlanRowInsert(table, row)
	require.NoError(t, err)
	require.NoError(t, db.Put(plan.Primary.Key, []byte("corrupt")))
	key := row[1]
	encoded, err := EncodeCommand(Command{Operations: []CommandOperation{{Kind: CommandDeleteRow, Table: &table, PrimaryKey: &key}}})
	require.NoError(t, err)
	result, err := db.ApplyCommand(encoded)
	require.ErrorIs(t, err, ErrWriteFailed)
	require.ErrorIs(t, err, ErrInvalidData)
	require.Empty(t, result.Code)
	require.False(t, errors.Is(err, ErrInvalidCommand))
}
