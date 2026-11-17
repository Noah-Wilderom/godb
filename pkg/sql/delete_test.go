package sql

import (
	"context"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
)

func TestSQLDeleteIndexesCountsAndReopen(t *testing.T) {
	db, path := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,name TEXT UNIQUE,tenant BIGINT,KEY by_tenant(tenant),UNIQUE KEY pair(tenant,name))")
	execute(t, engine, "INSERT INTO t(id,name,tenant) VALUES(1,'alice',7)")
	execute(t, engine, "INSERT INTO t(id,name,tenant) VALUES(2,'bob',7)")
	require.Equal(t, Result{AffectedRows: 1, MatchedRows: 1}, execute(t, engine, "DELETE FROM t WHERE id=?", record.NewInt64(1)))
	require.Empty(t, execute(t, engine, "SELECT * FROM t WHERE id=1").Rows)
	require.Empty(t, execute(t, engine, "SELECT * FROM t WHERE name='alice'").Rows)
	require.Empty(t, execute(t, engine, "SELECT * FROM t WHERE tenant=7 AND name='alice'").Rows)
	require.Equal(t, [][]record.Value{{record.NewInt64(2)}}, execute(t, engine, "SELECT id FROM t WHERE tenant=7").Rows)

	for _, query := range []string{"DELETE FROM t WHERE id=1", "DELETE FROM t WHERE id=99", "DELETE FROM t WHERE id=NULL"} {
		require.Equal(t, Result{}, execute(t, engine, query))
	}

	require.Equal(t, float64(1), testutil.ToFloat64(engine.metrics.deleted))
	require.Equal(t, float64(3), testutil.ToFloat64(engine.metrics.affected))
	require.Zero(t, testutil.ToFloat64(engine.metrics.matched))
	execute(t, engine, "INSERT INTO t(id,name,tenant) VALUES(1,'alice',7)")
	require.NoError(t, db.Close())
	require.NoError(t, database.Validate(path))
	reopened, err := database.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	engine, err = NewLocal(reopened)
	require.NoError(t, err)
	require.Len(t, execute(t, engine, "SELECT * FROM t WHERE tenant=7 AND name='alice'").Rows, 1)
	require.Equal(t, Result{AffectedRows: 1, MatchedRows: 1}, execute(t, engine, "DELETE FROM t WHERE id=1"))
}

func TestSQLDeletePreparationAndConcurrentExecution(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,name TEXT UNIQUE)")
	before := db.Stats().Writes
	plan, err := Prepare(db, "DELETE FROM t WHERE id=1", PrepareOptions{})
	require.NoError(t, err)
	require.Equal(t, before, db.Stats().Writes)
	command, err := plan.Command()
	require.NoError(t, err)
	execute(t, engine, "INSERT INTO t(id,name) VALUES(1,'old')")
	execute(t, engine, "UPDATE t SET name='new' WHERE id=1")

	var workers sync.WaitGroup

	outcomes := make(chan database.CommandResult, 2)
	failures := make(chan error, 2)

	for range 2 {
		workers.Go(func() { outcome, err := db.ApplyCommand(command); outcomes <- outcome; failures <- err })
	}

	workers.Wait()
	close(outcomes)
	close(failures)

	for err := range failures {
		require.NoError(t, err)
	}

	var affected uint64

	for result := range outcomes {
		require.Equal(t, database.CommandOK, result.Code)
		require.Equal(t, result.AffectedRows, result.MatchedRows)
		affected += result.AffectedRows
	}

	require.Equal(t, uint64(1), affected)
	require.Empty(t, execute(t, engine, "SELECT * FROM t WHERE name='old'").Rows)
	require.Empty(t, execute(t, engine, "SELECT * FROM t WHERE name='new'").Rows)
}

func TestSQLDeleteValidationAndDescribe(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT UNSIGNED PRIMARY KEY,name TEXT UNIQUE)")
	execute(t, engine, "INSERT INTO t(id,name) VALUES(1,'keep')")
	before := db.Stats().Writes
	description, err := Describe(db, "DELETE FROM t WHERE id=?")
	require.NoError(t, err)
	require.Empty(t, description.Columns)
	require.Len(t, description.Parameters, 1)
	require.Equal(t, record.TypeUint64, description.Parameters[0].Type)
	require.True(t, description.Parameters[0].Nullable)

	for _, test := range []struct {
		query  string
		args   []record.Value
		target error
	}{
		{"DELETE FROM t WHERE name='keep'", nil, ErrUnsupported},
		{"DELETE FROM t WHERE absent=1", nil, ErrColumn},
		{"DELETE FROM t WHERE id=-1", nil, ErrType},
		{"DELETE FROM t WHERE id='1'", nil, ErrType},
		{"DELETE FROM t WHERE id=?", nil, ErrBinding},
		{"DELETE FROM t WHERE id=?", []record.Value{record.NewBool(true)}, ErrType},
		{"DELETE FROM t WHERE id=1", []record.Value{record.NewInt64(1)}, ErrBinding},
		{"DELETE FROM missing WHERE id=1", nil, database.ErrTableNotFound},
	} {
		_, err := engine.Execute(context.Background(), test.query, test.args...)
		require.ErrorIs(t, err, test.target, test.query)
	}

	for _, query := range []string{
		"DELETE", "DELETE t WHERE id=1", "DELETE FROM t", "DELETE FROM t WHERE", "DELETE FROM t WHERE id>1",
		"DELETE FROM t WHERE id=1 AND name='keep'", "DELETE FROM t WHERE id=1 OR id=2", "DELETE FROM t WHERE id=1 LIMIT 1",
		"DELETE FROM t WHERE id=1 ORDER BY id", "DELETE FROM t WHERE id=1 RETURNING id", "DELETE FROM t,u WHERE id=1",
		"DELETE FROM t WHERE id=1; DELETE FROM t WHERE id=2",
	} {
		_, err := Parse(query)
		require.ErrorIs(t, err, ErrSyntax, query)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = engine.Execute(ctx, "DELETE FROM t WHERE id=1")
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, before, db.Stats().Writes)
	require.Len(t, execute(t, engine, "SELECT * FROM t WHERE id=1").Rows, 1)
}

func TestSQLDeleteTextPrimaryKey(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE names(name TEXT PRIMARY KEY)")
	key := textValue(t, "O'Reilly\x00;-- café")
	execute(t, engine, "INSERT INTO names(name) VALUES(?)", key)
	require.Equal(t, Result{AffectedRows: 1, MatchedRows: 1}, execute(t, engine, "DELETE FROM names WHERE name=?", key))
	require.Empty(t, execute(t, engine, "SELECT * FROM names WHERE name=?", key).Rows)
}
