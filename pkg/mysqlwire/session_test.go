package mysqlwire

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/sql"
)

func TestSessionSettingsAreAtomicAndConnectionScoped(t *testing.T) {
	s, _, _, calls := newTestSession(t)
	other := newStatementSession(s.endpoint)

	ctx := context.Background()
	for _, query := range []string{
		"SET NAMES utf8mb4 COLLATE utf8mb4_bin", "SET SESSION autocommit = ON", "SET @@session.autocommit=TRUE",
		"SET LOCAL character_set_client='utf8mb4', character_set_connection=\"utf8mb4\"", "USE `godb`;",
	} {
		_, err := s.query(ctx, query)
		require.NoError(t, err, query)
	}

	_, err := s.query(ctx, "SET character_set_results=NULL")
	require.NoError(t, err)
	require.True(t, s.state.resultsNull)
	require.False(t, other.state.resultsNull)

	for _, query := range []string{
		"SET character_set_results=utf8mb4,autocommit=0", "SET character_set_results=utf8mb4; SELECT 1",
		"SET NAMES latin1", "SET NAMES utf8mb4 COLLATE utf8mb4_general_ci", "SET GLOBAL autocommit=1",
		"SET @@global.autocommit=1", "SET @user=1", "SET sql_mode=''", "SET time_zone='+00:00'",
		"SET transaction_isolation='READ-COMMITTED'", "SET autocommit=NULL", "SET autocommit=DEFAULT", "USE missing",
	} {
		_, err := s.query(ctx, query)
		require.Error(t, err, query)
		require.True(t, s.state.resultsNull, query)
	}

	_, err = s.query(ctx, "SET NAMES utf8mb4")
	require.NoError(t, err)
	require.False(t, s.state.resultsNull)
	require.Zero(t, *calls)
	require.Equal(t, uint64(13), s.endpoint.sessionErrors.Load())
}

func TestSessionMetadataAndPreparedBindings(t *testing.T) {
	s, _, _, calls := newTestSession(t)
	s.state.id = 42
	ctx := context.Background()
	query := "SELECT VERSION() AS version, DATABASE() AS db, CONNECTION_ID() AS id, @@session.autocommit, @@character_set_results, 1"
	description, err := s.describe(ctx, query)
	require.NoError(t, err)
	result, err := s.query(ctx, query)
	require.NoError(t, err)
	require.Equal(t, description.Columns, result.Columns)
	require.Equal(t, "version", result.Columns[0].Name)
	require.Equal(t, record.NewUint64(42), result.Rows[0][2])
	require.Equal(t, record.NewInt64(1), result.Rows[0][3])

	description, err = s.describe(ctx, "SET character_set_results=?")
	require.NoError(t, err)
	require.Len(t, description.Parameters, 1)
	require.True(t, description.Parameters[0].Nullable)
	require.False(t, s.state.resultsNull)
	_, err = s.query(ctx, "SET character_set_results=?", record.NewNull())
	require.NoError(t, err)
	result, err = s.query(ctx, query)
	require.NoError(t, err)
	require.True(t, result.Rows[0][4].IsNull())
	_, err = s.query(ctx, "SET autocommit=?", record.NewInt64(0))
	require.Error(t, err)
	_, err = s.query(ctx, "SET autocommit=?", record.NewInt64(1))
	require.NoError(t, err)
	_, err = s.query(ctx, "SET autocommit=?")
	require.Error(t, err)
	_, err = s.query(ctx, "SELECT 1", record.NewInt64(1))
	require.Error(t, err)
	_, err = s.describe(ctx, "SET sql_mode=?")
	require.Error(t, err)
	require.Zero(t, *calls)
}

func TestSessionVariableListing(t *testing.T) {
	s, _, _, _ := newTestSession(t)
	for _, entry := range []struct {
		query string
		count int
	}{
		{"SHOW VARIABLES", 9},
		{"SHOW SESSION VARIABLES LIKE 'character_set_%'", 3},
		{"SHOW VARIABLES LIKE 'VERSION%'", 2},
		{"SHOW VARIABLES LIKE 'missing'", 0},
		{"SHOW VARIABLES LIKE 'autocommi_'", 1},
	} {
		result, err := s.query(context.Background(), entry.query)
		require.NoError(t, err)
		require.Len(t, result.Rows, entry.count)
		description, err := s.describe(context.Background(), entry.query)
		require.NoError(t, err)
		require.Equal(t, description.Columns, result.Columns)
		_, err = resultPackets("godb", result)
		require.NoError(t, err)
		_, err = binaryResultPackets("godb", result)
		require.NoError(t, err)
	}
}

func TestResetConnectionReleasesSessionResources(t *testing.T) {
	s, p, conn, _ := newTestSession(t)
	s.state.id = 42
	ctx := context.Background()
	_, err := s.query(ctx, "SET character_set_results=NULL")
	require.NoError(t, err)
	require.NoError(t, s.prepare(ctx, p, "SET character_set_results=?"))
	require.NoError(t, s.appendLong(longCommand(1, 0, []byte("utf8mb4"))))
	require.Positive(t, s.endpoint.longDataBytes.Load())
	conn.Reset()
	require.Error(t, s.handle(ctx, p, []byte{0x1f, 0}))
	require.Len(t, s.statements, 1)
	require.True(t, s.state.resultsNull)
	require.NoError(t, s.handle(ctx, p, []byte{0x1f}))
	require.Equal(t, []byte{0, 0, 0, 2, 0, 0, 0}, conn.Bytes()[4:])
	require.Equal(t, uint32(42), s.state.id)
	require.False(t, s.state.resultsNull)
	require.Empty(t, s.statements)
	require.Zero(t, s.endpoint.prepared.Load())
	require.Zero(t, s.endpoint.statementBytes.Load())
	require.Zero(t, s.endpoint.longDataBytes.Load())
	require.Equal(t, uint64(1), s.endpoint.resets.Load())
	conn.Reset()
	require.NoError(t, s.handle(ctx, p, append(stmtCommand(0x17, 1), 0, 1, 0, 0, 0)))
	require.Equal(t, uint16(1243), responseCode(t, conn))
	conn.Reset()
	require.NoError(t, s.prepare(ctx, p, "SELECT 1"))
	require.Contains(t, s.statements, uint32(2))
}

func TestSessionParsingRejectsUnsupportedTailsAndLimits(t *testing.T) {
	s, _, _, _ := newTestSession(t)
	for _, query := range []string{
		"SET", "SET database=godb", "SELECT @@one", "SELECT @@`version()`", "SELECT @@", "SELECT VERSION(", "SELECT @@session.", "SELECT 1 FROM secret", "SELECT 1; SELECT 1",
		"SELECT 1 -- ignored", "SELECT VERSION() UNION SELECT 1", "SELECT @@global.version", "SELECT @@unknown",
		"SET autocommit=1,", "SET autocommit=1 garbage", "SET NAMES 'utf8mb4' junk", "SET autocommit=1\x00",
		"SHOW GLOBAL VARIABLES", "SHOW VARIABLES WHERE 1=1", "SHOW VARIABLES LIKE '\\%'", "SELECT 2",
		"SELECT VERSION() AS `" + strings.Repeat("a", 65) + "`", "SET " + strings.Repeat(" ", sql.MaxStatementBytes),
		"SELECT " + strings.Repeat("1,", 64) + "1",
	} {
		_, err := s.query(context.Background(), query)
		require.Error(t, err, query)
	}

	for _, query := range []string{"SELECT version FROM t WHERE id=1", "SELECT * FROM t WHERE id=?", "INSERT INTO t (id) VALUES(1)", "SELECT `version` FROM t WHERE id=1"} {
		_, local, _ := parseSession(query)
		require.False(t, local, query)
	}
}

func FuzzSessionStatements(f *testing.F) {
	for _, query := range []string{"SET NAMES utf8mb4", "SELECT @@session.autocommit", "SET character_set_results=?", "SHOW VARIABLES LIKE '%'", "SELECT VERSION() AS v", "USE godb", "SELECT"} {
		f.Add(query)
	}

	f.Fuzz(func(t *testing.T, query string) {
		statement, local, err := parseSession(query)
		if err != nil || !local {
			return
		}

		s, _, _, _ := newTestSession(t)
		_, _ = s.runSession(statement, true, nil)
		_, _ = s.runSession(statement, false, nil)
	})
}

func BenchmarkSessionInformation(b *testing.B) {
	s, _, _, _ := newTestSession(b)
	for b.Loop() {
		_, err := s.query(context.Background(), "SELECT VERSION(), CONNECTION_ID(), @@autocommit")
		if err != nil {
			b.Fatal(err)
		}
	}
}
