package database

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	recordpkg "github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/schema"
)

func commandFixture() Command {
	table, _ := rowInsertFixture()
	first, second := numberedRow(1), numberedRow(2)
	updated := maps.Clone(first)
	updated[2] = recordpkg.NewBytes([]byte("changed"))
	key := second[1]
	return Command{Operations: []CommandOperation{
		{Kind: CommandCreateTable, Table: &table},
		{Kind: CommandInsertRow, Table: &table, Row: first},
		{Kind: CommandInsertRow, Table: &table, Row: second},
		{Kind: CommandUpdateRow, Table: &table, Row: updated},
		{Kind: CommandDeleteRow, Table: &table, PrimaryKey: &key},
		{Kind: CommandPut, Key: []byte("raw"), Value: []byte("value")},
		{Kind: CommandDelete, Key: []byte("raw")},
		{Kind: CommandPut, Key: nil, Value: nil},
	}}
}

func TestCommandKnownBytes(t *testing.T) {
	command := Command{Operations: []CommandOperation{{Kind: CommandPut, Key: []byte("k"), Value: []byte("v")}}}
	encoded, err := EncodeCommand(command)
	require.NoError(t, err)
	known, err := hex.DecodeString("474f4442434d44310100010000000006000000010000006b76")
	require.NoError(t, err)
	require.Equal(t, known, encoded)
	decoded, err := DecodeCommand(known)
	require.NoError(t, err)
	require.Equal(t, command, decoded)
}

func TestCommandRoundTripOwnershipAndAllOperations(t *testing.T) {
	command := commandFixture()
	encoded, err := EncodeCommand(command)
	require.NoError(t, err)
	want := bytes.Clone(encoded)
	decoded, err := DecodeCommand(encoded)
	require.NoError(t, err)
	clear(encoded)

	for _, op := range command.Operations {
		clear(op.Row)
		clear(op.Key)
		clear(op.Value)

		if op.Table != nil {
			op.Table.Name = "changed"
		}
	}

	again, err := EncodeCommand(decoded)
	require.NoError(t, err)
	require.Equal(t, want, again)
	require.Len(t, decoded.Operations, 8)
}

func TestCommandCanonicalSchemaAndMapOrder(t *testing.T) {
	table, row := rowInsertFixture()
	table.Columns = append(table.Columns, schema.Column{ID: 100, Name: "attributes", Type: recordpkg.TypeMap})
	row[100] = recordpkg.NewMap(map[string]recordpkg.Value{"z": recordpkg.NewInt64(1), "a": recordpkg.NewArray([]recordpkg.Value{recordpkg.NewNull(), recordpkg.NewBool(true)})})
	command := Command{Operations: []CommandOperation{{Kind: CommandInsertRow, Table: &table, Row: row}}}
	encoded, err := EncodeCommand(command)
	require.NoError(t, err)
	slices.Reverse(table.Columns)
	slices.Reverse(table.Indexes)
	row[100] = recordpkg.NewMap(map[string]recordpkg.Value{"a": recordpkg.NewArray([]recordpkg.Value{recordpkg.NewNull(), recordpkg.NewBool(true)}), "z": recordpkg.NewInt64(1)})
	again, err := EncodeCommand(command)
	require.NoError(t, err)
	require.Equal(t, encoded, again)
	require.Equal(t, uint32(100), table.Columns[0].ID)
}

func TestCommandRejectsInvalidInputAndBudget(t *testing.T) {
	table, row := rowInsertFixture()

	key := row[1]
	for _, command := range []Command{
		{},
		{Operations: make([]CommandOperation, MaxBatchEntries+1)},
		{Operations: []CommandOperation{{Kind: 255}}},
		{Operations: []CommandOperation{{Kind: CommandInsertRow, Table: &table}}},
		{Operations: []CommandOperation{{Kind: CommandInsertRow, Row: row}}},
		{Operations: []CommandOperation{{Kind: CommandDeleteRow, Table: &table}}},
		{Operations: []CommandOperation{{Kind: CommandDelete, Value: []byte("unexpected")}}},
		{Operations: []CommandOperation{{Kind: CommandPut, Table: &table}}},
		{Operations: []CommandOperation{{Kind: CommandPut, Row: row}}},
		{Operations: []CommandOperation{{Kind: CommandPut, PrimaryKey: &key}}},
		{Operations: []CommandOperation{{Kind: CommandPut, Value: make([]byte, 4096)}}},
		{Operations: []CommandOperation{{Kind: CommandCreateTable, Table: &table, Key: []byte("unexpected")}}},
	} {
		encoded, err := EncodeCommand(command)
		require.ErrorIs(t, err, ErrInvalidCommand)
		require.Nil(t, encoded)
	}

	command := Command{}
	for range 16 {
		command.Operations = append(command.Operations, CommandOperation{Kind: CommandInsertRow, Table: &table, Row: row})
	}

	_, err := EncodeCommand(command)
	require.NoError(t, err)
	command.Operations = append(command.Operations, CommandOperation{Kind: CommandPut})
	_, err = EncodeCommand(command)
	require.ErrorIs(t, err, ErrInvalidCommand)
	require.ErrorIs(t, err, ErrBatchTooLarge)
}

func TestDecodeCommandRejectsTruncationAndMalformedFields(t *testing.T) {
	encoded, err := EncodeCommand(commandFixture())
	require.NoError(t, err)

	for end := range len(encoded) {
		_, err := DecodeCommand(encoded[:end])
		require.ErrorIs(t, err, ErrInvalidCommand, "length %d", end)
	}

	for _, mutate := range []func([]byte) []byte{
		func(b []byte) []byte { b[7] = '2'; return b },
		func(b []byte) []byte { b[8], b[9] = 0, 0; return b },
		func(b []byte) []byte { b[8], b[9] = 255, 255; return b },
		func(b []byte) []byte { b[10] = 255; return b },
		func(b []byte) []byte { binary.LittleEndian.PutUint32(b[11:15], ^uint32(0)); return b },
		func(b []byte) []byte { return append(b, 0) },
	} {
		_, err := DecodeCommand(mutate(bytes.Clone(encoded)))
		require.ErrorIs(t, err, ErrInvalidCommand)
	}

	_, err = DecodeCommand(make([]byte, MaxCommandBytes+1))
	require.ErrorIs(t, err, ErrInvalidCommand)
}

func TestDecodeCommandRejectsNoncanonicalSchemas(t *testing.T) {
	table, _ := rowInsertFixture()
	canonical, err := EncodeCommand(Command{Operations: []CommandOperation{{Kind: CommandCreateTable, Table: &table}}})
	require.NoError(t, err)
	decoded, err := DecodeCommand(canonical)
	require.NoError(t, err)

	for _, mode := range []string{"Unsorted", "Whitespace", "UnknownField", "DuplicateField"} {
		t.Run(mode, func(t *testing.T) {
			table := cloneTableDefinition(*decoded.Operations[0].Table)
			if mode == "Unsorted" {
				slices.Reverse(table.Columns)
			}

			payload, err := json.Marshal(table)
			require.NoError(t, err)

			switch mode {
			case "Whitespace":
				payload = append([]byte(" "), payload...)
			case "UnknownField":
				payload = append([]byte(`{"Unknown":1,`), payload[1:]...)
			case "DuplicateField":
				payload = append([]byte(`{"ID":1,`), payload[1:]...)
			}

			wire := append(bytes.Clone(canonical[:11]), binary.LittleEndian.AppendUint32(nil, uint32(len(payload)))...)
			wire = append(wire, payload...)
			wire = binary.LittleEndian.AppendUint32(wire, 0)
			_, err = DecodeCommand(wire)
			require.ErrorIs(t, err, ErrInvalidCommand)
		})
	}
}

func FuzzDecodeCommand(f *testing.F) {
	encoded, err := EncodeCommand(commandFixture())
	if err != nil {
		f.Fatal(err)
	}

	f.Add(encoded)
	table, _ := rowInsertFixture()
	f.Add(patchCommand(f, table, recordpkg.NewUint64(1), schema.Row{3: recordpkg.NewInt64(5)}))
	f.Add(countedDeleteCommand(f, table, recordpkg.NewUint64(1)))
	f.Add(countedDeleteCommand(f, table, recordpkg.NewNull()))
	f.Add([]byte("GODBCMD1"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		command, err := DecodeCommand(data)
		if err != nil {
			require.ErrorIs(t, err, ErrInvalidCommand)
			return
		}

		again, err := EncodeCommand(command)
		require.NoError(t, err)
		require.Equal(t, data, again)
	})
}
