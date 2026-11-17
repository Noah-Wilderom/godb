package server

import (
	"testing"

	driver "github.com/go-sql-driver/mysql"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestMySQLUpdateChangedAndFoundRows(t *testing.T) {
	for _, foundRows := range []bool{false, true} {
		name := "changed"
		if foundRows {
			name = "found"
		}

		t.Run(name, func(t *testing.T) {
			c := setupCluster(t, 1)
			enableMySQL(t, c, 1)
			c.start(t, 1)
			c.leader(t)
			db := mysqlClientConfig(t, c, 1, "godb", "test-password", "godb", func(config *driver.Config) { config.ClientFoundRows = foundRows })
			_, err := db.Exec("CREATE TABLE t(id BIGINT PRIMARY KEY,name TEXT UNIQUE,note TEXT)")
			require.NoError(t, err)

			for _, query := range []string{"INSERT INTO t(id,name,note) VALUES(1,'alice','keep')", "INSERT INTO t(id,name) VALUES(2,'bob')"} {
				result, err := db.Exec(query)
				require.NoError(t, err)
				count, err := result.RowsAffected()
				require.NoError(t, err)
				require.Equal(t, int64(1), count)
			}

			update, err := db.Prepare("UPDATE t SET name=? WHERE id=?")
			require.NoError(t, err)
			defer func() { require.NoError(t, update.Close()) }()

			value := "O'Reilly\x00;-- café"
			for i := range 2 {
				result, err := update.Exec(value, int64(1))
				require.NoError(t, err)
				count, err := result.RowsAffected()
				require.NoError(t, err)

				want := int64(1)
				if i == 1 && !foundRows {
					want = 0
				}

				require.Equal(t, want, count)
			}

			for _, key := range []any{int64(999), nil} {
				result, err := update.Exec(value, key)
				require.NoError(t, err)
				count, err := result.RowsAffected()
				require.NoError(t, err)
				require.Zero(t, count)
			}

			var note string

			require.NoError(t, db.QueryRow("SELECT note FROM t WHERE name=?", value).Scan(&note))
			require.Equal(t, "keep", note)
			rows, err := db.Query("SELECT id FROM t WHERE name='alice'")
			require.NoError(t, err)
			_, values := readDiscoveryRows(t, rows)
			require.Empty(t, values)
			_, err = update.Exec("bob", int64(1))

			var wire *driver.MySQLError

			require.ErrorAs(t, err, &wire)
			require.Equal(t, uint16(1062), wire.Number)

			for i := range 2 {
				result, err := db.Exec("UPDATE t SET note='new' WHERE id=1")
				require.NoError(t, err)
				count, err := result.RowsAffected()
				require.NoError(t, err)

				want := int64(1)
				if i == 1 && !foundRows {
					want = 0
				}

				require.Equal(t, want, count)
			}

			require.Equal(t, float64(4), testutil.ToFloat64(c.members[1].metrics.sqlAffected))
			require.Equal(t, float64(4), testutil.ToFloat64(c.members[1].metrics.sqlMatched))
			require.NoError(t, db.Ping())
		})
	}
}
