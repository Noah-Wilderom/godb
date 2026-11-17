package sql

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSQLRejectsUnsupportedOrIncompleteSyntax(t *testing.T) {
	for _, query := range []string{
		"", "SELECT", "CREATE TABLE", "INSERT INTO",
		"CREATE TABLE t ()", "CREATE TABLE t (id BIGINT)",
		"CREATE TABLE t (id VARCHAR(10) PRIMARY KEY)",
		"CREATE TABLE t (id BIGINT PRIMARY KEY AUTO_INCREMENT)",
		"CREATE TABLE t (id BIGINT PRIMARY KEY,)",
		"CREATE TABLE t (id BIGINT PRIMARY KEY, x BIGINT PRIMARY KEY)",
		"CREATE TABLE t (id BIGINT PRIMARY KEY, PRIMARY KEY(id))",
		"CREATE TABLE t (id BIGINT NOT NULL NULL PRIMARY KEY)",
		"CREATE TABLE t (id BIGINT, x BIGINT, PRIMARY KEY(id,x))",
		"INSERT INTO t VALUES (1)", "INSERT INTO t (id) VALUES (1),(2)",
		"INSERT INTO t (id,x) VALUES (1)", "INSERT INTO t (id) VALUES (1,2)",
		"INSERT INTO t (id) VALUES (1.5)", "INSERT INTO t (id) VALUES (1+2)",
		"INSERT INTO t (id) VALUES ('unclosed)", `INSERT INTO t (id) VALUES ('a\b')`,
		"SELECT * FROM t WHERE", "SELECT * FROM t WHERE id <> 1", "SELECT * FROM t WHERE id=1 OR id=2",
		"SELECT * FROM t WHERE id=1 ORDER BY", "SELECT * FROM t WHERE id=1 LIMIT",
		"SELECT * FROM t WHERE id=1; DROP TABLE t", "SELECT * FROM t WHERE id=1;;",
		"SELECT * FROM t WHERE id=1 -- ignored", "SELECT * FROM t WHERE id=1 /* comment */",
		"SELECT * FROM t WHERE id=1\x00", "SELECT * FROM t WHERE id='\xff'",
	} {
		t.Run(query, func(t *testing.T) { _, err := Parse(query); require.Error(t, err) })
	}
}

func TestSQLParserLimits(t *testing.T) {
	_, err := Parse(strings.Repeat(" ", MaxStatementBytes+1))
	require.ErrorIs(t, err, ErrLimit)
	_, err = Parse(strings.Repeat("? ", MaxTokens+1))
	require.ErrorIs(t, err, ErrLimit)

	columns := []string{"id BIGINT PRIMARY KEY"}
	for i := range MaxColumns {
		columns = append(columns, fmt.Sprintf("c%d TEXT", i))
	}

	_, err = Parse("CREATE TABLE t (" + strings.Join(columns, ",") + ")")
	require.ErrorIs(t, err, ErrLimit)
	_, err = Parse("SELECT * FROM `" + strings.Repeat("x", MaxIdentifierBytes+1) + "` WHERE id=1")
	require.Error(t, err)
}

func FuzzParseSQL(f *testing.F) {
	for _, query := range []string{"EXPLAIN SELECT * FROM t WHERE id>=? LIMIT ?", "EXPLAIN ANALYZE SELECT * FROM t", "EXPLAIN EXPLAIN SELECT * FROM t", "SELECT * FROM t WHERE id>=? AND id<?", "SELECT COUNT(*) FROM t WHERE name<=? LIMIT ?", "SELECT id FROM t WHERE id < = 1", "SELECT COUNT(*) FROM t WHERE name IS NULL", "SELECT id FROM t WHERE name IS NOT NULL AND active=? LIMIT ? OFFSET ?", "SELECT id FROM t WHERE id IS NOT", "SELECT COUNT(*) AS aggregate FROM t", "SELECT COUNT(name) FROM t WHERE active=? LIMIT ? OFFSET ?", "SELECT count FROM t", "SELECT COUNT(DISTINCT name) FROM t", "SELECT * FROM t", "SELECT * FROM t WHERE active=? ORDER BY id LIMIT ? OFFSET ?", "SELECT id FROM t LIMIT 0", "DELETE FROM t WHERE id=?", "DELETE FROM t WHERE id=NULL", "DELETE FROM t", "UPDATE t SET x=?,y=NULL WHERE id=?", "UPDATE t SET x=1 WHERE id=NULL", "SELECT * FROM t WHERE a=? ORDER BY b DESC,id ASC LIMIT ? OFFSET ?", "SELECT * FROM t WHERE a=1 LIMIT ?,?", "SELECT * FROM t WHERE a=? AND b=1 AND a=NULL", "CREATE TABLE t (id BIGINT PRIMARY KEY,x TEXT UNIQUE,UNIQUE KEY pair(id,x),INDEX lookup(x))", "CREATE TABLE t (CONSTRAINT named UNIQUE(x),id BIGINT PRIMARY KEY,x TEXT)", "CREATE TABLE t (id BIGINT PRIMARY KEY)", "INSERT INTO t (id) VALUES (?)", "SELECT * FROM t WHERE id='a''b'", "", "`", "\xff", "CREATE TABLE t (PRIMARY KEY(id))"} {
		f.Add(query)
	}

	f.Fuzz(func(t *testing.T, query string) {
		statement, err := Parse(query)
		if err == nil {
			require.Contains(t, []Kind{CreateTable, Insert, Select, Update, Delete, Explain}, statement.Kind())
			require.LessOrEqual(t, statement.parameters, MaxTokens)

			if statement.Kind() == CreateTable {
				_, _ = prepareCreate(statement, 1)
			}
		}
	})
}
