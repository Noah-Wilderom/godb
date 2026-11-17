package sql

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/record"
)

func TestSQLUpdatePartialIndexesAndReopen(t *testing.T) {
	db, path := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE accounts(id BIGINT PRIMARY KEY,name TEXT UNIQUE,tenant BIGINT,note TEXT,active BOOL NOT NULL,KEY by_tenant(tenant),UNIQUE KEY pair(tenant,name))")
	execute(t, engine, "INSERT INTO accounts(id,name,tenant,note,active) VALUES(1,'alice',7,'keep',TRUE)")
	execute(t, engine, "INSERT INTO accounts(id,name,tenant,active) VALUES(2,'bob',8,FALSE)")
	result := execute(t, engine, "UPDATE accounts SET name=?,tenant=?,active=? WHERE id=?", textValue(t, "new"), record.NewInt64(9), record.NewBool(false), record.NewInt64(1))
	require.Equal(t, Result{AffectedRows: 1, MatchedRows: 1}, result)
	require.Empty(t, execute(t, engine, "SELECT * FROM accounts WHERE name='alice'").Rows)
	require.Empty(t, execute(t, engine, "SELECT * FROM accounts WHERE tenant=7").Rows)
	require.Equal(t, [][]record.Value{{record.NewInt64(1), textValue(t, "keep"), record.NewBool(false)}}, execute(t, engine, "SELECT id,note,active FROM accounts WHERE tenant=9 AND name='new'").Rows)
	require.Equal(t, Result{MatchedRows: 1}, execute(t, engine, "UPDATE accounts SET name='new' WHERE id=1"))
	require.Equal(t, Result{}, execute(t, engine, "UPDATE accounts SET name='missing' WHERE id=999"))
	require.Equal(t, Result{}, execute(t, engine, "UPDATE accounts SET name='missing' WHERE id=NULL"))
	_, err = engine.Execute(context.Background(), "UPDATE accounts SET name='bob',note='lost' WHERE id=1")

	var rejected *CommandError

	require.ErrorAs(t, err, &rejected)
	require.Equal(t, database.CommandUniqueConflict, rejected.Code)
	_, err = engine.Execute(context.Background(), "UPDATE accounts SET note=? WHERE id=1", textValue(t, strings.Repeat("x", 5000)))
	require.ErrorAs(t, err, &rejected)
	require.Equal(t, database.CommandInvalidUpdate, rejected.Code)
	require.Equal(t, [][]record.Value{{textValue(t, "keep")}}, execute(t, engine, "SELECT note FROM accounts WHERE id=1").Rows)
	require.Equal(t, Result{AffectedRows: 1, MatchedRows: 1}, execute(t, engine, "UPDATE accounts SET name=NULL WHERE id=1"))
	require.Empty(t, execute(t, engine, "SELECT * FROM accounts WHERE name='new'").Rows)
	require.Equal(t, float64(4), testutil.ToFloat64(engine.metrics.affected))
	require.Equal(t, float64(3), testutil.ToFloat64(engine.metrics.matched))
	require.NoError(t, db.Close())
	require.NoError(t, database.Validate(path))
	reopened, err := database.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	engine, err = NewLocal(reopened)
	require.NoError(t, err)
	require.Equal(t, [][]record.Value{{record.NewNull(), record.NewInt64(9), textValue(t, "keep")}}, execute(t, engine, "SELECT name,tenant,note FROM accounts WHERE id=1").Rows)
}

func TestSQLUpdatePreparationPreservesConcurrentColumns(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT PRIMARY KEY,a BIGINT,b BIGINT)")
	first, err := Prepare(db, "UPDATE t SET a=10 WHERE id=1", PrepareOptions{})
	require.NoError(t, err)
	second, err := Prepare(db, "UPDATE t SET b=20 WHERE id=1", PrepareOptions{})
	require.NoError(t, err)
	execute(t, engine, "INSERT INTO t(id,a,b) VALUES(1,0,0)")

	var workers sync.WaitGroup

	outcomes := make(chan database.CommandResult, 2)
	failures := make(chan error, 2)

	for _, plan := range []*Plan{first, second} {
		command, err := plan.Command()
		require.NoError(t, err)
		workers.Go(func() { outcome, err := db.ApplyCommand(command); outcomes <- outcome; failures <- err })
	}

	workers.Wait()
	close(outcomes)
	close(failures)

	for err := range failures {
		require.NoError(t, err)
	}

	for result := range outcomes {
		require.Equal(t, database.CommandResult{Code: database.CommandOK, AffectedRows: 1, MatchedRows: 1}, result)
	}

	require.Equal(t, [][]record.Value{{record.NewInt64(10), record.NewInt64(20)}}, execute(t, engine, "SELECT a,b FROM t WHERE id=1").Rows)
	left, err := Prepare(db, "UPDATE t SET a=?,b=? WHERE id=?", PrepareOptions{}, record.NewInt64(4), record.NewInt64(5), record.NewInt64(1))
	require.NoError(t, err)
	right, err := Prepare(db, "UPDATE t SET b=?,a=? WHERE id=?", PrepareOptions{}, record.NewInt64(5), record.NewInt64(4), record.NewInt64(1))
	require.NoError(t, err)
	a, err := left.Command()
	require.NoError(t, err)
	b, err := right.Command()
	require.NoError(t, err)
	require.Equal(t, a, b)
}

func TestSQLUpdateValidationAndDescribe(t *testing.T) {
	db, _ := openSQLDatabase(t)
	engine, err := NewLocal(db)
	require.NoError(t, err)
	execute(t, engine, "CREATE TABLE t(id BIGINT UNSIGNED PRIMARY KEY,name TEXT NOT NULL,active BOOL)")
	before := db.Stats().Writes
	description, err := Describe(db, "UPDATE t SET name=?,active=? WHERE id=?")
	require.NoError(t, err)
	require.Empty(t, description.Columns)
	require.Len(t, description.Parameters, 3)
	require.Equal(t, record.TypeText, description.Parameters[0].Type)
	require.False(t, description.Parameters[0].Nullable)
	require.Equal(t, record.TypeBool, description.Parameters[1].Type)
	require.True(t, description.Parameters[1].Nullable)
	require.Equal(t, record.TypeUint64, description.Parameters[2].Type)
	require.True(t, description.Parameters[2].Nullable)

	for _, test := range []struct {
		query  string
		target error
	}{
		{"UPDATE t SET missing=1 WHERE id=1", ErrColumn},
		{"UPDATE t SET id=2 WHERE id=1", ErrUnsupported},
		{"UPDATE t SET name='a',name='b' WHERE id=1", ErrColumn},
		{"UPDATE t SET name=NULL WHERE id=1", ErrType},
		{"UPDATE t SET active=2 WHERE id=1", ErrType},
		{"UPDATE t SET name='a' WHERE id=-1", ErrType},
		{"UPDATE t SET name='a' WHERE name='b'", ErrUnsupported},
		{"UPDATE t SET name='a' WHERE absent=1", ErrColumn},
		{"UPDATE t SET name=? WHERE id=?", ErrBinding},
	} {
		_, err := engine.Execute(context.Background(), test.query)
		require.ErrorIs(t, err, test.target, test.query)
	}

	for _, query := range []string{"UPDATE t SET name='x'", "UPDATE t SET WHERE id=1", "UPDATE t SET name=name WHERE id=1", "UPDATE t SET active=active+1 WHERE id=1", "UPDATE t SET name='a' WHERE id=1 AND active=TRUE", "UPDATE t SET name='a' WHERE id=1 LIMIT 1", "UPDATE t SET name='a' WHERE id=1 ORDER BY id", "UPDATE t SET name='a', WHERE id=1"} {
		_, err := Parse(query)
		require.ErrorIs(t, err, ErrSyntax, query)
	}

	_, err = Parse("UPDATE t SET " + strings.Repeat("name='x',", MaxColumns) + "name='x' WHERE id=1")
	require.ErrorIs(t, err, ErrLimit)
	require.Equal(t, before, db.Stats().Writes)
}
