package server

import (
	"context"
	"database/sql"
	"testing"

	driver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

func TestMySQLDriverSessionInitialization(t *testing.T) {
	c := setupCluster(t, 1)
	enableMySQL(t, c, 1)
	c.start(t, 1)
	c.leader(t)
	db := mysqlClientConfig(t, c, 1, "godb", "test-password", "godb", func(config *driver.Config) {
		config.MaxAllowedPacket = 0
		config.Params = map[string]string{"autocommit": "1", "character_set_results": "'utf8mb4'"}
		require.NoError(t, config.Apply(driver.Charset("utf8mb4", "utf8mb4_bin")))
	})
	require.NoError(t, db.Ping())
	ctx := context.Background()
	first, err := db.Conn(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, first.Close()) }()
	otherDB := mysqlClient(t, c, 1, "godb", "test-password", "godb")

	var (
		firstID, otherID  uint64
		version, database string
		auto              int
	)

	require.NoError(t, first.QueryRowContext(ctx, "SELECT VERSION(), DATABASE(), CONNECTION_ID(), @@session.autocommit").Scan(&version, &database, &firstID, &auto))
	require.Equal(t, "8.0.0-GoDB", version)
	require.Equal(t, "godb", database)
	require.Equal(t, 1, auto)
	require.NoError(t, otherDB.QueryRow("SELECT CONNECTION_ID()").Scan(&otherID))
	require.NotEqual(t, firstID, otherID)
	_, err = first.ExecContext(ctx, "SET character_set_results=?", nil)
	require.NoError(t, err)
	query, err := first.PrepareContext(ctx, "SELECT @@character_set_results")
	require.NoError(t, err)
	defer func() { require.NoError(t, query.Close()) }()

	var charset sql.NullString

	require.NoError(t, query.QueryRowContext(ctx).Scan(&charset))
	require.False(t, charset.Valid)
	require.NoError(t, otherDB.QueryRow("SELECT @@character_set_results").Scan(&charset))
	require.Equal(t, sql.NullString{String: "utf8mb4", Valid: true}, charset)
	_, err = first.ExecContext(ctx, "SET NAMES utf8mb4")
	require.NoError(t, err)
	require.NoError(t, query.QueryRowContext(ctx).Scan(&charset))
	require.Equal(t, sql.NullString{String: "utf8mb4", Valid: true}, charset)

	show, err := first.PrepareContext(ctx, "SHOW VARIABLES LIKE 'character_set_%'")
	require.NoError(t, err)
	defer func() { require.NoError(t, show.Close()) }()
	rows, err := show.QueryContext(ctx)
	require.NoError(t, err)
	count := 0

	for rows.Next() {
		var name, value string

		require.NoError(t, rows.Scan(&name, &value))
		require.Equal(t, "utf8mb4", value)
		count++
	}

	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Equal(t, 3, count)

	for _, query := range []string{"SET autocommit=0", "SET sql_mode='STRICT_TRANS_TABLES'", "SET time_zone='+00:00'", "SELECT 1; SELECT 1"} {
		_, err := first.ExecContext(ctx, query)

		var wire *driver.MySQLError

		require.ErrorAs(t, err, &wire, query)
	}

	_, err = first.BeginTx(ctx, nil)
	require.Error(t, err)

	var one int

	require.NoError(t, first.QueryRowContext(ctx, "SELECT 1").Scan(&one))
	require.Equal(t, 1, one)
	families, err := c.members[1].metrics.registry.Gather()
	require.NoError(t, err)
	observed := 0

	for _, family := range families {
		switch family.GetName() {
		case "godb_mysql_session_commands_total", "godb_mysql_session_errors_total":
			require.Positive(t, family.Metric[0].Counter.GetValue())
			observed++
		}
	}

	require.Equal(t, 2, observed)
}

func TestMySQLSessionInformationDoesNotPromiseQuorum(t *testing.T) {
	c := setupCluster(t, 3)
	for id := uint64(1); id <= 3; id++ {
		enableMySQL(t, c, id)
		c.start(t, id)
	}

	leader := c.leader(t)
	follower := leader%3 + 1
	db := mysqlClient(t, c, follower, "godb", "test-password", "godb")

	var one int

	require.NoError(t, db.QueryRow("SELECT 1").Scan(&one))
	_, err := db.Exec("CREATE TABLE follower_session (id BIGINT PRIMARY KEY)")

	var wire *driver.MySQLError

	require.ErrorAs(t, err, &wire)
	require.Equal(t, uint16(1290), wire.Number)
}
