package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	stdsql "database/sql"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/pkg/record"
)

func enableMySQL(t testing.TB, c *testCluster, id uint64) {
	t.Helper()
	password := filepath.Join(t.TempDir(), "password")
	require.NoError(t, os.WriteFile(password, []byte("test-password\n"), 0o600))
	config := c.configs[id]
	config.MySQL = MySQLConfig{Address: "127.0.0.1:0", User: "godb", PasswordFile: password, Database: "godb", QueryTimeout: time.Second}
	c.configs[id] = config
}

func mysqlClient(t testing.TB, c *testCluster, id uint64, user, password, database string) *stdsql.DB {
	t.Helper()
	return mysqlClientConfig(t, c, id, user, password, database, nil)
}

func mysqlClientConfig(t testing.TB, c *testCluster, id uint64, user, password, database string, configure func(*driver.Config)) *stdsql.DB {
	t.Helper()
	ca, err := os.ReadFile(c.ca.Path)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(ca))
	config := driver.NewConfig()
	config.Net = "tcp"
	config.Addr = c.members[id].MySQLAddress()
	config.User = user
	config.Passwd = password
	config.DBName = database
	config.TLS = &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "127.0.0.1"}
	config.Timeout = time.Second
	config.ReadTimeout = 3 * time.Second

	config.WriteTimeout = time.Second
	if configure != nil {
		configure(config)
	}

	connector, err := driver.NewConnector(config)
	require.NoError(t, err)
	db := stdsql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func TestMySQLDriverRoundTrip(t *testing.T) {
	c := setupCluster(t, 1)
	enableMySQL(t, c, 1)
	c.start(t, 1)
	c.leader(t)
	db := mysqlClient(t, c, 1, "godb", "test-password", "godb")
	require.NoError(t, db.Ping())
	_, err := db.Exec("CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, name TEXT NOT NULL, enabled BOOLEAN, note TEXT)")
	require.NoError(t, err)
	result, err := db.Exec("INSERT INTO users (id,name,enabled) VALUES (18446744073709551615,'Noah',TRUE)")
	require.NoError(t, err)
	affected, err := result.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)

	var (
		id, name, enabled string
		note              stdsql.NullString
	)

	require.NoError(t, db.QueryRow("SELECT id,name,enabled,note FROM users WHERE id=18446744073709551615").Scan(&id, &name, &enabled, &note))
	require.Equal(t, "18446744073709551615", id)
	require.Equal(t, "Noah", name)
	require.Equal(t, "1", enabled)
	require.False(t, note.Valid)
	rows, err := db.Query("SELECT name FROM users WHERE id=1")
	require.NoError(t, err)
	columns, err := rows.Columns()
	require.NoError(t, err)
	require.Equal(t, []string{"name"}, columns)
	require.False(t, rows.Next())
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	_, err = db.Exec("INSERT INTO users (id,name) VALUES (18446744073709551615,'duplicate')")

	var wire *driver.MySQLError

	require.ErrorAs(t, err, &wire)
	require.Equal(t, uint16(1062), wire.Number)
	require.Equal(t, "23000", string(wire.SQLState[:]))
	_, err = db.Exec("DELETE FROM users")
	require.ErrorAs(t, err, &wire)
	require.Equal(t, uint16(1064), wire.Number)
	stmt, err := db.Prepare("SELECT name FROM users WHERE id=?")
	require.NoError(t, err)
	require.NoError(t, stmt.QueryRow(uint64(18446744073709551615)).Scan(&name))
	require.Equal(t, "Noah", name)
	require.NoError(t, stmt.Close())
	require.NoError(t, db.Ping())
	table, found, err := c.members[1].db.GetTableByName("users")
	require.NoError(t, err)
	require.True(t, found)
	row, found, err := c.members[1].db.GetRow(table, record.NewUint64(^uint64(0)))
	require.NoError(t, err)
	require.True(t, found)
	value, err := row[2].Text()
	require.NoError(t, err)
	require.Equal(t, "Noah", value)
	metrics, err := c.members[1].metrics.registry.Gather()
	require.NoError(t, err)
	observed := false

	for _, metric := range metrics {
		if metric.GetName() == "godb_mysql_sessions" {
			observed = true
			require.Equal(t, float64(1), metric.Metric[0].GetGauge().GetValue())
		}

		require.NotContains(t, metric.String(), "test-password")
	}

	require.True(t, observed)
	require.NoError(t, db.Close())
	require.NoError(t, c.members[1].Close())
}

func TestMySQLAuthenticationAndShutdown(t *testing.T) {
	c := setupCluster(t, 1)
	enableMySQL(t, c, 1)
	c.start(t, 1)
	c.leader(t)

	for _, entry := range []struct {
		user, password, database string
		code                     uint16
	}{
		{"godb", "wrong", "godb", 1045}, {"other", "test-password", "godb", 1045}, {"godb", "test-password", "wrong", 1049},
	} {
		db := mysqlClient(t, c, 1, entry.user, entry.password, entry.database)

		var wire *driver.MySQLError

		require.ErrorAs(t, db.Ping(), &wire)
		require.Equal(t, entry.code, wire.Number)
	}

	plaintext := driver.NewConfig()
	plaintext.Net = "tcp"
	plaintext.Addr = c.members[1].MySQLAddress()
	plaintext.User = "godb"
	plaintext.Passwd = "test-password"
	connector, err := driver.NewConnector(plaintext)
	require.NoError(t, err)
	plain := stdsql.OpenDB(connector)
	defer func() { require.NoError(t, plain.Close()) }()
	require.Error(t, plain.Ping())
	active := mysqlClient(t, c, 1, "godb", "test-password", "godb")
	require.NoError(t, active.Ping())
	stalled, err := net.Dial("tcp", c.members[1].MySQLAddress())
	require.NoError(t, err)
	defer func() { require.NoError(t, stalled.Close()) }()
	done := make(chan error, 1)
	go func() { done <- c.members[1].Close() }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown waited for an idle or handshaking client")
	}

	require.Error(t, active.Ping())
}

func TestMySQLLeaderRoutingAndQuorumLoss(t *testing.T) {
	c := setupCluster(t, 3)
	for id := uint64(1); id <= 3; id++ {
		enableMySQL(t, c, id)
		c.start(t, id)
	}

	leader := c.leader(t)
	db := mysqlClient(t, c, leader, "godb", "test-password", "godb")
	_, err := db.Exec("CREATE TABLE items (id BIGINT PRIMARY KEY)")
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO items (id) VALUES (?)", int64(7))
	require.NoError(t, err)
	follower := mysqlClient(t, c, leader%3+1, "godb", "test-password", "godb")

	var value int64

	err = follower.QueryRow("SELECT id FROM items WHERE id=7").Scan(&value)

	var wire *driver.MySQLError

	require.ErrorAs(t, err, &wire)
	require.Equal(t, uint16(1290), wire.Number)

	read, err := db.Prepare("SELECT id FROM items WHERE id=?")
	require.NoError(t, err)
	defer func() { require.NoError(t, read.Close()) }()

	for id, member := range c.members {
		if id != leader {
			require.NoError(t, member.Close())
		}
	}

	err = read.QueryRow(int64(7)).Scan(&value)
	require.ErrorAs(t, err, &wire)
	require.True(t, wire.Number == 1290 || wire.Number == 3024)
	require.Zero(t, value)
}

func TestMySQLInvalidStartupCleansUp(t *testing.T) {
	c := setupCluster(t, 1)
	enableMySQL(t, c, 1)
	config := c.configs[1]
	require.NoError(t, os.WriteFile(config.MySQL.PasswordFile, nil, 0o600))
	_, err := Start(context.Background(), config, c.listeners[1])
	require.ErrorIs(t, err, ErrInvalidConfig)
	require.False(t, errors.Is(err, context.Canceled))
	require.NoError(t, os.WriteFile(config.MySQL.PasswordFile, []byte("test-password"), 0o600))
	c.start(t, 1)
	c.leader(t)
}

func TestMySQLPDOConnection(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("PHP is not installed")
	}

	c := setupCluster(t, 1)
	enableMySQL(t, c, 1)
	c.start(t, 1)
	c.leader(t)
	host, port, err := net.SplitHostPort(c.members[1].MySQLAddress())
	require.NoError(t, err)
	script := `if (!extension_loaded('pdo_mysql')) { exit(77); }
 $sslCA=defined('Pdo\Mysql::ATTR_SSL_CA') ? constant('Pdo\Mysql::ATTR_SSL_CA') : PDO::MYSQL_ATTR_SSL_CA;
 $sslVerify=defined('Pdo\Mysql::ATTR_SSL_VERIFY_SERVER_CERT') ? constant('Pdo\Mysql::ATTR_SSL_VERIFY_SERVER_CERT') : PDO::MYSQL_ATTR_SSL_VERIFY_SERVER_CERT;
 $db = new PDO(getenv('GODB_TEST_DSN'), 'godb', 'test-password', [
 PDO::ATTR_ERRMODE => PDO::ERRMODE_EXCEPTION,
 PDO::ATTR_EMULATE_PREPARES => false,
 $sslCA => getenv('GODB_TEST_CA'),
 $sslVerify => true,
 ]);
 $db->exec('SET NAMES utf8mb4 COLLATE utf8mb4_bin');
 $db->exec('SET SESSION autocommit=1');
 $info=$db->query('SELECT VERSION(), DATABASE(), CONNECTION_ID(), @@autocommit')->fetch(PDO::FETCH_NUM);
 if ($info[0] !== '8.0.0-GoDB' || $info[1] !== 'godb' || $info[2] < 1 || $info[3] != 1) { exit(4); }
 $settings=$db->prepare('SET character_set_results=?');
 $settings->bindValue(1,null,PDO::PARAM_NULL);
 $settings->execute();
 if ($db->query('SELECT @@character_set_results')->fetchColumn() !== null) { exit(5); }
 $db->exec('SET NAMES utf8mb4');
 if (count($db->query("SHOW VARIABLES LIKE 'character_set_%'")->fetchAll()) !== 3) { exit(6); }
 try { $db->exec('SET autocommit=0'); exit(7); } catch (PDOException $e) {}
 $db->exec('CREATE TABLE pdo_test (id BIGINT PRIMARY KEY, name TEXT NOT NULL)');
 $tables=$db->prepare('SHOW FULL TABLES LIKE ?');
 $tables->execute(['pdo_test']);
 if ($tables->fetch(PDO::FETCH_NUM) !== ['pdo_test','BASE TABLE']) { exit(8); }
 $tables->closeCursor();
 $columns=$db->query('DESCRIBE pdo_test')->fetchAll(PDO::FETCH_ASSOC);
 if (count($columns)!==2 || $columns[0]['Key']!=='PRI' || $columns[1]['Type']!=='text') { exit(9); }
 $indexes=$db->query('SHOW INDEX FROM pdo_test')->fetchAll(PDO::FETCH_ASSOC);
 if (count($indexes)!==1 || $indexes[0]['Key_name']!=='PRIMARY' || $indexes[0]['Cardinality']!==null) { exit(10); }
 $meta=$db->prepare('SELECT COLUMN_NAME AS name,DATA_TYPE,COLUMN_DEFAULT FROM information_schema.columns WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? ORDER BY ORDINAL_POSITION');
 $meta->execute(['pdo_test']);
 $fields=$meta->fetchAll(PDO::FETCH_ASSOC);
 if (count($fields)!==2 || $fields[0]['name']!=='id' || $fields[1]['DATA_TYPE']!=='text' || $fields[0]['COLUMN_DEFAULT']!==null) { exit(11); }
 $exists=$db->prepare('SELECT COUNT(*) AS aggregate FROM information_schema.tables WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?');
 $exists->execute(['pdo_test']);
 if ((int)$exists->fetchColumn()!==1) { exit(12); }
 $exists->closeCursor();
 $constraints=$db->prepare('SELECT CONSTRAINT_NAME,CONSTRAINT_TYPE,ENFORCED FROM information_schema.table_constraints WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?');
 $constraints->execute(['pdo_test']);
 if ($constraints->fetch(PDO::FETCH_NUM)!==['PRIMARY','PRIMARY KEY','YES']) { exit(13); }
 $constraints->closeCursor();
 $keys=$db->prepare('SELECT COLUMN_NAME,ORDINAL_POSITION,POSITION_IN_UNIQUE_CONSTRAINT,REFERENCED_TABLE_NAME FROM information_schema.key_column_usage WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? ORDER BY ORDINAL_POSITION');
 $keys->execute(['pdo_test']);
 $key=$keys->fetch(PDO::FETCH_NUM);
 if ($key[0]!=='id' || (int)$key[1]!==1 || $key[2]!==null || $key[3]!==null) { exit(14); }
 $keys->closeCursor();
 $insert=$db->prepare('INSERT INTO pdo_test (id,name) VALUES (?,?)');
 $name="PDO's \path\0?; DROP TABLE pdo_test; -- café";
 $insert->bindValue(1,7,PDO::PARAM_INT);
 $insert->bindValue(2,$name,PDO::PARAM_STR);
 $insert->execute();
 $query=$db->prepare('SELECT name FROM pdo_test WHERE id=?');
 $query->bindValue(1,7,PDO::PARAM_INT);
 $query->execute();
 if ($query->fetchColumn() !== $name) { exit(2); }
 $query->closeCursor();
 $stream=fopen('php://memory','r+');
 fwrite($stream, "stream's \0\data");
 rewind($stream);
 $insert->bindValue(1,8,PDO::PARAM_INT);
 $insert->bindParam(2,$stream,PDO::PARAM_LOB);
 $insert->execute();
 $query->bindValue(1,8,PDO::PARAM_INT);
 $query->execute();
 if ($query->fetchColumn() !== "stream's \0\data") { exit(3); }
 if (is_resource($stream)) { fclose($stream); }
 $ddl=$db->prepare('CREATE TABLE pdo_indexed (id BIGINT PRIMARY KEY, name TEXT UNIQUE, tenant BIGINT, UNIQUE KEY tenant_name(tenant,name), INDEX lookup_name(name))');
 $ddl->execute();
 $db->exec("INSERT INTO pdo_indexed (id,name,tenant) VALUES (1,'alice',7)");
 try { $db->exec("INSERT INTO pdo_indexed (id,name,tenant) VALUES (2,'alice',8)"); exit(15); } catch (PDOException $e) { if ($e->errorInfo[1]!==1062) { exit(16); } }
 $indexes=$db->query("SHOW INDEX FROM pdo_indexed")->fetchAll(PDO::FETCH_ASSOC);
 if (count($indexes)!==5) { exit(17); }
 $db->exec("INSERT INTO pdo_indexed (id,name,tenant) VALUES (2,'bob',7)");
 $indexed=$db->prepare('SELECT name FROM pdo_indexed WHERE tenant=?');
 $indexed->bindValue(1,7,PDO::PARAM_INT);
 $indexed->execute();
 $names=$indexed->fetchAll(PDO::FETCH_COLUMN);
 sort($names);
 if ($names!==['alice','bob']) { exit(18); }
 $unique=$db->prepare('SELECT id FROM pdo_indexed WHERE name=?');
 $unique->execute(['bob']);
 if ((int)$unique->fetchColumn()!==2) { exit(19); }
 $composite=$db->prepare('SELECT id FROM pdo_indexed WHERE name=? AND tenant=?');
 $composite->bindValue(1,'bob',PDO::PARAM_STR);
 $composite->bindValue(2,7,PDO::PARAM_INT);
 $composite->execute();
 if ((int)$composite->fetchColumn()!==2) { exit(20); }
 $composite->bindValue(2,8,PDO::PARAM_INT);
 $composite->execute();
 if ($composite->fetchAll(PDO::FETCH_NUM)!==[]) { exit(21); }
 $ordered=$db->prepare('SELECT name FROM pdo_indexed WHERE tenant=? ORDER BY name DESC LIMIT ? OFFSET ?');
 $ordered->bindValue(1,7,PDO::PARAM_INT);
 $ordered->bindValue(2,1,PDO::PARAM_INT);
 $ordered->bindValue(3,1,PDO::PARAM_INT);
 $ordered->execute();
 if ($ordered->fetchAll(PDO::FETCH_COLUMN)!==['alice']) { exit(22); }
 $ordered->bindValue(3,0,PDO::PARAM_INT);
 $ordered->execute();
 if ($ordered->fetchAll(PDO::FETCH_COLUMN)!==['bob']) { exit(23); }
 $update=$db->prepare('UPDATE pdo_indexed SET name=? WHERE id=?');
 $update->bindValue(1,'renamed',PDO::PARAM_STR);
 $update->bindValue(2,2,PDO::PARAM_INT);
 $update->execute();
 if ($update->rowCount()!==1) { exit(24); }
 $update->execute();
 if ($update->rowCount()!==0) { exit(25); }
 $update->bindValue(2,99,PDO::PARAM_INT);
 $update->execute();
 if ($update->rowCount()!==0) { exit(26); }
 $unique->execute(['renamed']);
 if ((int)$unique->fetchColumn()!==2) { exit(27); }
 $delete=$db->prepare('DELETE FROM pdo_indexed WHERE id=?');
 $delete->bindValue(1,2,PDO::PARAM_INT);
 $delete->execute();
 if ($delete->rowCount()!==1) { exit(28); }
 $delete->execute();
 if ($delete->rowCount()!==0) { exit(29); }
 $delete->bindValue(1,null,PDO::PARAM_NULL);
 $delete->execute();
 if ($delete->rowCount()!==0) { exit(30); }
 $unique->execute(['renamed']);
 if ($unique->fetchAll(PDO::FETCH_COLUMN)!==[]) { exit(31); }
 $scan=$db->prepare('SELECT name FROM pdo_test WHERE name=? ORDER BY id LIMIT ? OFFSET ?');
 $scan->bindValue(1,$name,PDO::PARAM_STR);
 $scan->bindValue(2,1,PDO::PARAM_INT);
 $scan->bindValue(3,0,PDO::PARAM_INT);
 $scan->execute();
 if ($scan->fetchAll(PDO::FETCH_COLUMN)!==[$name]) { exit(32); }
 $scanAll=$db->prepare('SELECT name FROM pdo_test ORDER BY id DESC LIMIT ?');
 $scanAll->bindValue(1,1,PDO::PARAM_INT);
 $scanAll->execute();
 if ($scanAll->fetchColumn()!=="stream's \0\data") { exit(33); }
 if ($db->query('SELECT name FROM pdo_indexed')->fetchAll(PDO::FETCH_COLUMN)!==['alice']) { exit(34); }
 $counts=$db->prepare('SELECT COUNT(name) AS aggregate FROM pdo_test WHERE name=?');
 $counts->bindValue(1,$name,PDO::PARAM_STR);
 $counts->execute();
 if ((int)$counts->fetchColumn()!==1) { exit(35); }
 $counts->bindValue(1,null,PDO::PARAM_NULL);
 $counts->execute();
 if ((int)$counts->fetchColumn()!==0) { exit(36); }
 $counts->closeCursor();
 $total=$db->query('SELECT COUNT(*) AS aggregate FROM pdo_test')->fetch(PDO::FETCH_ASSOC);
 if ((int)$total['aggregate']!==2) { exit(37); }
 $countPage=$db->prepare('SELECT COUNT(*) FROM pdo_test LIMIT ? OFFSET ?');
 $countPage->bindValue(1,1,PDO::PARAM_INT);
 $countPage->bindValue(2,1,PDO::PARAM_INT);
 $countPage->execute();
 if ($countPage->fetchAll(PDO::FETCH_COLUMN)!==[]) { exit(38); }
 $db->exec('INSERT INTO pdo_indexed(id,tenant) VALUES(3,7)');
 $db->exec('INSERT INTO pdo_indexed(id,tenant) VALUES(4,7)');
 $nullRows=$db->prepare('SELECT id FROM pdo_indexed WHERE name IS NULL AND tenant=? ORDER BY id LIMIT ?');
 $nullRows->bindValue(1,7,PDO::PARAM_INT);
 $nullRows->bindValue(2,10,PDO::PARAM_INT);
 $nullRows->execute();
 if (array_map('intval',$nullRows->fetchAll(PDO::FETCH_COLUMN))!==[3,4]) { exit(39); }
 $nullCount=$db->prepare('SELECT COUNT(*) AS aggregate FROM pdo_indexed WHERE name IS NULL');
 $nullCount->execute();
 if ((int)$nullCount->fetchColumn()!==2) { exit(40); }
 $nullCount->closeCursor();
 if ($db->query('SELECT name FROM pdo_indexed WHERE name IS NOT NULL')->fetchAll(PDO::FETCH_COLUMN)!==['alice']) { exit(41); }
 if ((int)$db->query('SELECT COUNT(*) FROM pdo_indexed WHERE name=NULL')->fetchColumn()!==0) { exit(42); }
 $db->exec("UPDATE pdo_indexed SET name='charlie' WHERE id=3");
 $nullCount->execute();
 if ((int)$nullCount->fetchColumn()!==1) { exit(43); }
 $nullCount->closeCursor();
 if ((int)$db->query('SELECT COUNT(name) FROM pdo_indexed WHERE name IS NULL')->fetchColumn()!==0) { exit(44); }
 $range=$db->prepare('SELECT name FROM pdo_test WHERE id>=? AND id<? ORDER BY id LIMIT ?');
 $range->bindValue(1,7,PDO::PARAM_INT);
 $range->bindValue(2,9,PDO::PARAM_INT);
 $range->bindValue(3,10,PDO::PARAM_INT);
 $range->execute();
 $rangeRows=$range->fetchAll(PDO::FETCH_COLUMN);
 if (count($rangeRows)!==2 || $rangeRows[0]!==$name) { exit(45); }
 if ((int)$db->query('SELECT COUNT(*) FROM pdo_test WHERE id>7')->fetchColumn()!==1) { exit(46); }
 if ($db->query('SELECT name FROM pdo_indexed WHERE id>=3 AND name IS NOT NULL')->fetchAll(PDO::FETCH_COLUMN)!==['charlie']) { exit(47); }
 $rangeCount=$db->prepare('SELECT COUNT(*) FROM pdo_test WHERE id<=?');
 $rangeCount->bindValue(1,null,PDO::PARAM_NULL);
 $rangeCount->execute();
 if ((int)$rangeCount->fetchColumn()!==0) { exit(48); }
 $secondary=$db->prepare('SELECT name FROM pdo_indexed WHERE tenant=? AND name>? AND name<=? ORDER BY name');
 $secondary->bindValue(1,7,PDO::PARAM_INT);
 $secondary->bindValue(2,'alice',PDO::PARAM_STR);
 $secondary->bindValue(3,'charlie',PDO::PARAM_STR);
 $secondary->execute();
 if ($secondary->fetchAll(PDO::FETCH_COLUMN)!==['charlie']) { exit(49); }
 if ((int)$db->query("SELECT COUNT(*) FROM pdo_indexed WHERE tenant=7 AND name<='charlie'")->fetchColumn()!==2) { exit(50); }
 $secondary->bindValue(2,null,PDO::PARAM_NULL);
 $secondary->execute();
 if ($secondary->fetchAll(PDO::FETCH_COLUMN)!==[]) { exit(51); }
 $explain=$db->prepare('EXPLAIN SELECT name FROM pdo_indexed WHERE tenant=? AND name>=? LIMIT ?');
 $explain->bindValue(1,7,PDO::PARAM_INT);
 $explain->bindValue(2,'alice',PDO::PARAM_STR);
 $explain->bindValue(3,0,PDO::PARAM_INT);
 $explain->execute();
 $plan=$explain->fetch(PDO::FETCH_ASSOC);
 if ($plan['access']!=='index_range' || $plan['range_column']!=='name' || (int)$plan['candidate_limit']!==1024 || (int)$plan['result_limit']!==0) { exit(52); }
 if ($explain->fetch(PDO::FETCH_ASSOC)!==false) { exit(53); }
 $explain->bindValue(2,null,PDO::PARAM_NULL);
 $explain->execute();
 if ((int)$explain->fetch(PDO::FETCH_ASSOC)['candidate_limit']!==0) { exit(54); }
 $explain->closeCursor();
 if ($db->query('EXPLAIN SELECT COUNT(*) FROM pdo_indexed LIMIT 0')->fetch(PDO::FETCH_ASSOC)['extra']!=='COUNT(*)') { exit(55); }
 echo 'PDO round trip passed';`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, php, "-r", script)
	command.Env = append(os.Environ(), "GODB_TEST_DSN=mysql:host="+host+";port="+port+";dbname=godb;charset=utf8mb4", "GODB_TEST_CA="+c.ca.Path)
	output, err := command.CombinedOutput()

	var exit *exec.ExitError

	if errors.As(err, &exit) && exit.ExitCode() == 77 {
		t.Skip("pdo_mysql is not installed")
	}

	require.NoError(t, err, string(output))
	require.Contains(t, string(output), "PDO round trip passed")
}
