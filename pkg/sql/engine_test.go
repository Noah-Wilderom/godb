package sql

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"sync"
	"testing"
	"uuid"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
)

func openSQLDatabase(t testing.TB) (*database.Database, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sql.db")
	require.NoError(t, database.Create(path))
	db, err := database.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db, path
}

func textValue(t testing.TB, text string) record.Value {
	t.Helper()
	value, err := record.NewText(text)
	require.NoError(t, err)
	return value
}

func execute(t testing.TB, engine *LocalEngine, query string, args ...record.Value) Result {
	t.Helper()
	result, err := engine.Execute(context.Background(), query, args...)
	require.NoError(t, err)
	return result
}

func TestSQLRoundTripAndReopen(t *testing.T) {
	db, path := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE users (id BIGINT PRIMARY KEY, name TEXT NOT NULL, active BOOLEAN, note TEXT)")
	result := execute(t, engine, "INSERT INTO users (name, id, active) VALUES (?, ?, TRUE)", textValue(t, "O'Reilly; DROP TABLE users; --"), record.NewInt64(7))
	require.Equal(t, uint64(1), result.AffectedRows)
	result = execute(t, engine, "SELECT name, id, note, active FROM users WHERE id = ?", record.NewInt64(7))
	require.Len(t, result.Rows, 1)
	require.Equal(t, []record.Value{textValue(t, "O'Reilly; DROP TABLE users; --"), record.NewInt64(7), record.NewNull(), record.NewBool(true)}, result.Rows[0])
	require.Equal(t, "name", result.Columns[0].Name)
	require.Empty(t, execute(t, engine, "SELECT * FROM users WHERE id = 99").Rows)
	require.Empty(t, execute(t, engine, "SELECT * FROM users WHERE id = NULL").Rows)
	_, err = engine.Execute(context.Background(), "INSERT INTO users (id,name) VALUES (7,'duplicate')")

	var rejection *CommandError

	require.ErrorAs(t, err, &rejection)
	require.Equal(t, database.CommandDuplicatePrimaryKey, rejection.Code)
	require.ErrorIs(t, err, ErrRejected)
	require.NoError(t, db.Close())
	reopened, err := database.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	again, err := NewLocal(reopened)
	require.NoError(t, err)
	require.Len(t, execute(t, again, "SELECT * FROM users WHERE id=7").Rows, 1)
	execute(t, again, "CREATE TABLE more (id BIGINT PRIMARY KEY)")
	table, found, err := reopened.GetTableByName("more")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint32(2), table.ID)
	require.NoError(t, reopened.Close())
	require.NoError(t, database.Validate(path))
}

func TestSQLTypedValuesAndBindings(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE limits (id BIGINT UNSIGNED PRIMARY KEY, signed BIGINT NOT NULL, flag BOOL NOT NULL, note TEXT)")
	execute(t, engine, "INSERT INTO limits (id,signed,flag,note) VALUES (18446744073709551615,-9223372036854775808,1,'it''s valid')")
	result := execute(t, engine, "SELECT * FROM limits WHERE id=?", record.NewUint64(math.MaxUint64))
	require.Equal(t, []record.Value{record.NewUint64(math.MaxUint64), record.NewInt64(math.MinInt64), record.NewBool(true), textValue(t, "it's valid")}, result.Rows[0])

	for _, test := range []struct {
		query  string
		args   []record.Value
		target error
	}{
		{"INSERT INTO limits(id,signed,flag) VALUES (-1,0,TRUE)", nil, ErrType},
		{"INSERT INTO limits(id,signed,flag) VALUES (18446744073709551616,0,TRUE)", nil, ErrType},
		{"INSERT INTO limits(id,signed,flag) VALUES (1,9223372036854775808,TRUE)", nil, ErrType},
		{"INSERT INTO limits(id,signed,flag) VALUES (1,-9223372036854775809,TRUE)", nil, ErrType},
		{"INSERT INTO limits(id,signed,flag) VALUES (1,0,2)", nil, ErrType},
		{"INSERT INTO limits(id,signed,flag) VALUES (NULL,0,FALSE)", nil, ErrType},
		{"INSERT INTO limits(id,signed,flag) VALUES ('1',0,FALSE)", nil, ErrType},
		{"INSERT INTO limits(id,signed,flag) VALUES (1,?,FALSE)", []record.Value{record.NewUint64(math.MaxUint64)}, ErrType},
		{"INSERT INTO limits(id,signed,flag) VALUES (1,0,?)", nil, ErrBinding},
		{"SELECT * FROM limits WHERE id=?", []record.Value{record.NewInt64(1), record.NewInt64(2)}, ErrBinding},
		{"SELECT * FROM limits WHERE id=?", []record.Value{textValue(t, "0 OR 1=1")}, ErrType},
		{"SELECT * FROM limits WHERE missing=1", nil, ErrColumn},
		{"SELECT missing FROM limits WHERE id=1", nil, ErrColumn},
		{"INSERT INTO limits (id,id) VALUES (1,2)", nil, ErrColumn},
		{"INSERT INTO limits (id) VALUES (1)", nil, ErrBinding},
	} {
		t.Run(test.query, func(t *testing.T) {
			_, err := engine.Execute(context.Background(), test.query, test.args...)
			require.ErrorIs(t, err, test.target)
		})
	}

	require.Len(t, execute(t, engine, "SELECT * FROM limits WHERE id=18446744073709551615").Rows, 1)
	require.Empty(t, execute(t, engine, "SELECT * FROM limits WHERE id=1").Rows)
}

func TestSQLCreateValidationAndQuoting(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "create table `select` (`primary` TEXT, `a``b` BOOLEAN, PRIMARY KEY (`primary`));")
	execute(t, engine, "insert into `select` (`primary`,`a``b`) values ('你好',false)")
	result := execute(t, engine, "select `a``b`, `primary`, `primary` from `select` where `primary`='你好'")
	require.Equal(t, []record.Value{record.NewBool(false), textValue(t, "你好"), textValue(t, "你好")}, result.Rows[0])

	for _, query := range []string{
		"CREATE TABLE invalid (id BIGINT, id TEXT, PRIMARY KEY(id))",
		"CREATE TABLE invalid (id BIGINT, PRIMARY KEY(missing))",
		"CREATE TABLE invalid (id BOOL PRIMARY KEY)",
		"CREATE TABLE invalid (id BIGINT NULL PRIMARY KEY)",
		"CREATE TABLE invalid (id BIGINT NULL, PRIMARY KEY(id))",
	} {
		_, err := engine.Execute(context.Background(), query)
		require.Error(t, err)
		_, found, err := db.GetTableByName("invalid")
		require.NoError(t, err)
		require.False(t, found)
	}
}

func TestSQLPlansReplayThroughLog(t *testing.T) {
	left, _ := openSQLDatabase(t)
	right, _ := openSQLDatabase(t)
	group := uuid.UUID{1}
	ddl, err := Prepare(nil, "CREATE TABLE items (id BIGINT PRIMARY KEY, name TEXT NOT NULL)", PrepareOptions{CreateTableID: 42})
	require.NoError(t, err)
	encoded, err := ddl.Command()
	require.NoError(t, err)

	for _, db := range []*database.Database{left, right} {
		result, err := db.ApplyLogEntry(database.LogEntry{GroupID: group, Index: 1, Term: 1, RequestID: uuid.UUID{2}, Command: encoded})
		require.NoError(t, err)
		require.Equal(t, database.CommandOK, result.Code)
	}

	insert, err := Prepare(left, "INSERT INTO items (id,name) VALUES (?,?)", PrepareOptions{}, record.NewInt64(9), textValue(t, "replicated"))
	require.NoError(t, err)
	command, err := insert.Command()
	require.NoError(t, err)
	copyOfCommand, err := insert.Command()
	require.NoError(t, err)
	copyOfCommand[0] ^= 0xff
	again, err := insert.Command()
	require.NoError(t, err)
	require.Equal(t, command, again)

	for _, db := range []*database.Database{left, right} {
		result, err := db.ApplyLogEntry(database.LogEntry{GroupID: group, Index: 2, Term: 1, RequestID: uuid.UUID{3}, Command: command})
		require.NoError(t, err)
		require.Equal(t, database.CommandOK, result.Code)
		_, err = NewLocal(db)
		require.ErrorIs(t, err, ErrClusterExecution)
	}

	read, err := Prepare(left, "SELECT name FROM items WHERE id=9", PrepareOptions{})
	require.NoError(t, err)
	a, err := read.Read(left)
	require.NoError(t, err)
	b, err := read.Read(right)
	require.NoError(t, err)
	require.Equal(t, a, b)
	require.Len(t, a.Rows, 1)
	_, err = read.Command()
	require.ErrorIs(t, err, ErrUnsupported)
	_, err = ddl.Read(left)
	require.ErrorIs(t, err, ErrUnsupported)
}

func TestSQLConcurrentUseAndCancellation(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = engine.Execute(ctx, "CREATE TABLE canceled (id BIGINT PRIMARY KEY)")
	require.ErrorIs(t, err, context.Canceled)
	_, found, err := db.GetTableByName("canceled")
	require.NoError(t, err)
	require.False(t, found)
	execute(t, engine, "CREATE TABLE parallel (id BIGINT PRIMARY KEY)")

	var workers sync.WaitGroup

	errorsFound := make(chan error, 16)

	for i := range 16 {
		workers.Go(func() {
			_, err := engine.Execute(context.Background(), "INSERT INTO parallel (id) VALUES (?)", record.NewInt64(int64(i)))
			errorsFound <- err
		})
	}

	workers.Wait()
	close(errorsFound)

	for err := range errorsFound {
		require.NoError(t, err)
	}

	for i := range 16 {
		require.Len(t, execute(t, engine, "SELECT * FROM parallel WHERE id=?", record.NewInt64(int64(i))).Rows, 1)
	}

	registry := prometheus.NewPedanticRegistry()
	require.NoError(t, registry.Register(engine.Metrics()))
	metrics, err := registry.Gather()
	require.NoError(t, err)

	for _, metric := range metrics {
		require.NotContains(t, metric.String(), "parallel")
	}

	require.Zero(t, testutil.ToFloat64(engine.metrics.active))
	require.Equal(t, float64(16), testutil.ToFloat64(engine.metrics.rows))
	require.Equal(t, float64(16), testutil.ToFloat64(engine.metrics.affected))
	require.NoError(t, db.Close())
	_, err = engine.Execute(context.Background(), "SELECT * FROM parallel WHERE id=1")
	require.True(t, errors.Is(err, database.ErrClosed))
}
