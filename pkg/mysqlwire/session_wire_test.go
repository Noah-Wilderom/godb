package mysqlwire

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"net"
	"os"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/internal/testutil"
	"github.com/Noah-Wilderom/godb/pkg/record"
	"github.com/Noah-Wilderom/godb/pkg/sql"
)

func TestResetConnectionOverTLS(t *testing.T) {
	ca := testutil.NewAuthority(t)
	cert, key := ca.Issue(t, uuid.UUID{1}, 1)
	pair, err := tls.LoadX509KeyPair(cert, key)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	endpoint, err := New(listener, Config{TLS: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}}, User: "user", Database: "godb", PasswordHash: sha256.Sum256([]byte("secret"))}, Handler{Query: func(context.Context, string, ...record.Value) (sql.Result, error) {
		t.Error("session command reached the database")
		return sql.Result{}, nil
	}})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- endpoint.Serve(context.Background()) }()
	defer func() { endpoint.Close(); require.NoError(t, <-done) }()
	conn, err := net.Dial("tcp", endpoint.Address())
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
	p := &packets{conn: conn}
	_, err = p.read()
	require.NoError(t, err)
	request := binary.LittleEndian.AppendUint32(nil, capabilities)
	request = binary.LittleEndian.AppendUint32(request, maxPacket)
	request = append(request, 46)
	request = append(request, make([]byte, 23)...)
	require.NoError(t, p.write(request))
	pem, err := os.ReadFile(ca.Path)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(pem))
	secure := tls.Client(conn, &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "127.0.0.1"})
	require.NoError(t, secure.HandshakeContext(context.Background()))
	p.conn = secure
	login := append(request, []byte("user\x00\x00godb\x00"+authPlugin+"\x00")...)
	require.NoError(t, p.write(login))
	response, err := p.read()
	require.NoError(t, err)
	require.Equal(t, []byte{1, 4}, response)
	require.NoError(t, p.write([]byte("secret\x00")))
	response, err = p.read()
	require.NoError(t, err)
	require.Equal(t, byte(0), response[0])

	command := func(data []byte) []byte {
		t.Helper()
		p.sequence = 0
		require.NoError(t, p.write(data))
		response, err := p.read()
		require.NoError(t, err)
		return response
	}
	response = command(append([]byte{3}, "SET character_set_results=NULL"...))
	require.Equal(t, byte(0), response[0])
	response = command(append([]byte{0x16}, "SET character_set_results=?"...))
	require.Equal(t, byte(0), response[0])
	id := binary.LittleEndian.Uint32(response[1:5])
	_, err = p.read()
	require.NoError(t, err)
	_, err = p.read()
	require.NoError(t, err)
	p.sequence = 0
	require.NoError(t, p.write(longCommand(id, 0, []byte("utf8mb4"))))
	response = command([]byte{0x1f})
	require.Equal(t, []byte{0, 0, 0, 2, 0, 0, 0}, response)
	require.Zero(t, endpoint.prepared.Load())
	require.Zero(t, endpoint.longDataBytes.Load())
	require.Equal(t, uint64(1), endpoint.resets.Load())
	response = command(append(stmtCommand(0x17, id), 0, 1, 0, 0, 0))
	require.Equal(t, byte(0xff), response[0])
	require.Equal(t, uint16(1243), binary.LittleEndian.Uint16(response[1:3]))
	response = command(append([]byte{3}, "SELECT @@character_set_results"...))
	require.Equal(t, []byte{1}, response)
	_, err = p.read()
	require.NoError(t, err)
	_, err = p.read()
	require.NoError(t, err)
	response, err = p.read()
	require.NoError(t, err)
	require.Equal(t, appendString(nil, "utf8mb4"), response)
	response, err = p.read()
	require.NoError(t, err)
	require.Equal(t, []byte{0xfe, 0, 0, 2, 0}, response)
}
