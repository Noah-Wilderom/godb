package mysqlwire

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	stdsql "database/sql"
	"net"
	"os"
	"testing"
	"time"
	"uuid"

	driver "github.com/go-sql-driver/mysql"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/internal/testutil"
	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/sql"
)

func TestConnectionAdmissionAndHandshakeTimeout(t *testing.T) {
	ca := testutil.NewAuthority(t)
	cert, key := ca.Issue(t, uuid.UUID{1}, 1)
	pair, err := tls.LoadX509KeyPair(cert, key)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	endpoint, err := New(listener, Config{TLS: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}}, User: "user", Database: "godb", PasswordHash: sha256.Sum256([]byte("secret")), MaxConnections: 1, HandshakeTimeout: 200 * time.Millisecond}, Handler{Query: func(context.Context, string, ...record.Value) (sql.Result, error) {
		t.Error("unauthenticated query")
		return sql.Result{}, nil
	}})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- endpoint.Serve(context.Background()) }()
	defer func() { endpoint.Close(); require.NoError(t, <-done) }()
	conn, err := net.Dial("tcp", endpoint.Address())
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	require.NoError(t, conn.SetDeadline(time.Now().Add(time.Second)))
	_, err = (&packets{conn: conn}).read()
	require.NoError(t, err)
	second, err := net.Dial("tcp", endpoint.Address())
	require.NoError(t, err)
	require.NoError(t, second.SetDeadline(time.Now().Add(time.Second)))
	_, err = (&packets{conn: second}).read()
	require.Error(t, err)
	require.NoError(t, second.Close())
	require.Eventually(t, func() bool { return endpoint.open.Load() == 0 && endpoint.authFailures.Load() == 1 }, time.Second, time.Millisecond)
	require.Equal(t, uint64(1), endpoint.rejected.Load())
	require.Equal(t, int64(1), endpoint.peak.Load())
	registry := prometheus.NewPedanticRegistry()
	require.NoError(t, registry.Register(endpoint.Metrics()))
	_, err = registry.Gather()
	require.NoError(t, err)
}

func TestQueryDeadlineReturnsErrorAndKeepsConnection(t *testing.T) {
	ca := testutil.NewAuthority(t)
	cert, key := ca.Issue(t, uuid.UUID{1}, 1)
	pair, err := tls.LoadX509KeyPair(cert, key)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	endpoint, err := New(listener, Config{TLS: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}}, User: "user", Database: "godb", PasswordHash: sha256.Sum256([]byte("secret")), QueryTimeout: 30 * time.Millisecond}, Handler{Query: func(ctx context.Context, _ string, _ ...record.Value) (sql.Result, error) {
		<-ctx.Done()
		return sql.Result{}, &Error{3024, "HY000", "Query deadline exceeded"}
	}})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- endpoint.Serve(context.Background()) }()
	defer func() { endpoint.Close(); require.NoError(t, <-done) }()
	pem, err := os.ReadFile(ca.Path)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(pem))
	config := driver.NewConfig()
	config.Net = "tcp"
	config.Addr = endpoint.Address()
	config.User = "user"
	config.Passwd = "secret"
	config.DBName = "godb"
	config.TLS = &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "127.0.0.1"}
	config.ReadTimeout = time.Second
	connector, err := driver.NewConnector(config)
	require.NoError(t, err)
	db := stdsql.OpenDB(connector)
	defer func() { require.NoError(t, db.Close()) }()
	db.SetMaxOpenConns(1)
	_, err = db.Exec("SELECT ignored")

	var wire *driver.MySQLError

	require.ErrorAs(t, err, &wire)
	require.Equal(t, uint16(3024), wire.Number)
	require.NoError(t, db.Ping())
	require.Equal(t, uint64(1), endpoint.opened.Load())
}
