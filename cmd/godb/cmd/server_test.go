package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/require"

	"github.com/Noah-Wilderom/godb/internal/testutil"
	"github.com/Noah-Wilderom/godb/pkg/database"
)

type startupOutput struct {
	mu      sync.Mutex
	buffer  bytes.Buffer
	started chan struct{}
}

func (w *startupOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	n, err := w.buffer.Write(data)
	if strings.Contains(w.buffer.String(), "listening on") {
		select {
		case w.started <- struct{}{}:
		default:
		}
	}

	return n, err
}

func TestServerCommandStartupUsesStdoutAndStopsWithContext(t *testing.T) {
	group := uuid.UUID{1}
	ca := testutil.NewAuthority(t)
	cert, key := ca.Issue(t, group, 1)
	dbPath := filepath.Join(t.TempDir(), "data.db")
	require.NoError(t, database.Create(dbPath))
	passwordFile := filepath.Join(t.TempDir(), "password")
	require.NoError(t, os.WriteFile(passwordFile, []byte("test-password"), 0o600))
	command := newServerCommand()
	output := &startupOutput{started: make(chan struct{}, 1)}

	var errOutput bytes.Buffer

	command.SetOut(output)
	command.SetErr(&errOutput)
	command.SetArgs([]string{"--database", dbPath, "--group", group.String(), "--node", "1", "--peer", "1=127.0.0.1:12345", "--listen", "127.0.0.1:0", "--bootstrap", "--tls-cert", cert, "--tls-key", key, "--tls-ca", ca.Path, "--metrics-listen", "127.0.0.1:0", "--mysql-listen", "127.0.0.1:0", "--mysql-user", "test", "--mysql-password-file", passwordFile})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- command.ExecuteContext(ctx) }()
	select {
	case <-output.started:
	case err := <-done:
		t.Fatalf("server exited before startup: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}

	output.mu.Lock()
	require.Contains(t, output.buffer.String(), "Database member 1 listening on 127.0.0.1:")
	require.Contains(t, output.buffer.String(), "Prometheus metrics listening on http://127.0.0.1:")
	output.mu.Unlock()
	require.Contains(t, output.buffer.String(), "MySQL endpoint listening on 127.0.0.1:")
	require.NotContains(t, output.buffer.String(), "test-password")
	require.Empty(t, errOutput.String())
	require.NoError(t, database.Validate(dbPath))
}

func TestServerCommandPeerParsingAndRequiredFlags(t *testing.T) {
	peers, err := parseServerPeers([]string{"1=localhost:5001", "2=[::1]:5002"})
	require.NoError(t, err)
	require.Equal(t, map[uint64]string{1: "localhost:5001", 2: "[::1]:5002"}, peers)

	for _, values := range [][]string{{"bad"}, {"0=localhost:1"}, {"x=localhost:1"}, {"1="}, {"1=a:1", "1=b:2"}} {
		_, err := parseServerPeers(values)
		require.Error(t, err)
	}

	command := newServerCommand()
	command.SilenceErrors, command.SilenceUsage = true, true
	command.SetArgs([]string{})
	require.ErrorContains(t, command.Execute(), "required flag")
}
