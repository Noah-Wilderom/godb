package server

import (
	"testing"

	driver "github.com/go-sql-driver/mysql"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestMySQLDeleteChangedAndFoundRows(t *testing.T) {
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
			_, err := db.Exec("CREATE TABLE t(id BIGINT PRIMARY KEY,name TEXT UNIQUE,tenant BIGINT,INDEX by_tenant(tenant))")
			require.NoError(t, err)
			_, err = db.Exec("INSERT INTO t(id,name,tenant) VALUES(1,'alice',7)")
			require.NoError(t, err)
			statement, err := db.Prepare("DELETE FROM t WHERE id=?")
			require.NoError(t, err)
			defer func() { require.NoError(t, statement.Close()) }()

			var id int64

			require.NoError(t, db.QueryRow("SELECT id FROM t WHERE name='alice'").Scan(&id))
			require.Equal(t, int64(1), id)

			for _, key := range []any{"invalid", float64(1.5)} {
				_, err = statement.Exec(key)
				require.Error(t, err)
			}

			for _, query := range []string{"DELETE FROM t", "DELETE FROM t WHERE tenant=7"} {
				_, err = db.Exec(query)
				require.Error(t, err)
			}

			for i, key := range []any{int64(1), int64(1), nil, int64(999)} {
				result, err := statement.Exec(key)
				require.NoError(t, err)
				count, err := result.RowsAffected()
				require.NoError(t, err)

				want := int64(0)
				if i == 0 {
					want = 1
				}

				require.Equal(t, want, count)
			}

			for _, query := range []string{"SELECT id FROM t WHERE name='alice'", "SELECT id FROM t WHERE tenant=7"} {
				rows, err := db.Query(query)
				require.NoError(t, err)
				_, values := readDiscoveryRows(t, rows)
				require.Empty(t, values)
			}

			_, err = db.Exec("INSERT INTO t(id,name,tenant) VALUES(1,'alice',7)")
			require.NoError(t, err)

			for i := range 2 {
				result, err := db.Exec("DELETE FROM t WHERE id=1")
				require.NoError(t, err)
				count, err := result.RowsAffected()
				require.NoError(t, err)
				require.Equal(t, int64(1-i), count)
			}

			require.Equal(t, float64(2), testutil.ToFloat64(c.members[1].metrics.sqlDeleted))
			require.Equal(t, float64(4), testutil.ToFloat64(c.members[1].metrics.sqlAffected))
			require.Zero(t, testutil.ToFloat64(c.members[1].metrics.sqlMatched))
			require.NoError(t, db.Ping())
		})
	}
}
