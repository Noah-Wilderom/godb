package server

import (
	"context"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	peerv1 "github.com/Noah-Wilderom/godb/api/peer/v1"
)

func TestPeerConnectionMetricsCountTransportsNotRPCs(t *testing.T) {
	c := setupCluster(t, 2)
	c.start(t, 1)
	s := c.members[1]
	tlsConfig, err := loadTLS(c.configs[2])
	require.NoError(t, err)
	connection, err := grpc.NewClient("passthrough:///"+c.configs[1].Peers[1], grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig.client(1))))
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	require.Zero(t, s.network.inbound.connections.open.Load())
	client := peerv1.NewPeerClient(connection)
	require.NoError(t, callPeer(client, heartbeat(t, 2)))
	require.NoError(t, callPeer(client, heartbeat(t, 2)))
	bad := heartbeat(t, 2)
	bad.GroupId[0] = 99
	require.Error(t, callPeer(client, bad))
	rpc := &s.network.inbound
	require.Eventually(t, func() bool { return rpc.started.Load() == 3 && rpc.active.Load() == 0 && rpc.failed.Load() == 1 }, time.Second, time.Millisecond)
	require.Equal(t, int64(1), rpc.connections.open.Load())
	require.Equal(t, uint64(1), rpc.connections.opened.Load())
	require.Equal(t, int64(1), rpc.connections.peak.Load())
	require.Positive(t, rpc.received.Load())
	require.Positive(t, rpc.sent.Load())
	require.NoError(t, connection.Close())
	require.Eventually(t, func() bool { return rpc.connections.open.Load() == 0 && rpc.connections.closed.Load() == 1 }, time.Second, time.Millisecond)
}

func TestPeerConnectionsCloseOnServerShutdown(t *testing.T) {
	c := setupCluster(t, 3)
	for id := uint64(1); id <= 3; id++ {
		c.start(t, id)
	}

	c.leader(t)
	require.Eventually(t, func() bool {
		for _, s := range c.members {
			if s.network.inbound.connections.open.Load() == 0 || s.network.outbound.connections.open.Load() == 0 {
				return false
			}
		}

		return true
	}, 5*time.Second, time.Millisecond)

	for _, s := range c.members {
		require.NoError(t, s.Close())
	}

	t.Cleanup(func() {
		if t.Failed() {
			for id, s := range c.members {
				t.Logf("node %d: inbound=%d outbound=%d active inbound=%d active outbound=%d opened/closed inbound=%d/%d outbound=%d/%d", id, s.network.inbound.connections.open.Load(), s.network.outbound.connections.open.Load(), s.network.inbound.active.Load(), s.network.outbound.active.Load(), s.network.inbound.connections.opened.Load(), s.network.inbound.connections.closed.Load(), s.network.outbound.connections.opened.Load(), s.network.outbound.connections.closed.Load())
			}
		}
	})

	require.Eventually(t, func() bool {
		for _, s := range c.members {
			if s.network.inbound.connections.open.Load() != 0 || s.network.outbound.connections.open.Load() != 0 || s.network.inbound.active.Load() != 0 || s.network.outbound.active.Load() != 0 {
				return false
			}
		}

		return true
	}, time.Second, time.Millisecond)
}

func TestMetricsHTTPConnectionLifecycle(t *testing.T) {
	c := setupCluster(t, 1)
	config := c.configs[1]
	config.MetricsAddress = "127.0.0.1:0"
	c.configs[1] = config
	c.start(t, 1)
	s := c.members[1]
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)

	client := &http.Client{Transport: transport, Timeout: time.Second}
	for range 2 {
		response, err := client.Get("http://" + s.MetricsAddress() + "/metrics")
		require.NoError(t, err)
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Contains(t, string(body), `kind="metrics_http"`)
	}

	require.Equal(t, int64(1), s.network.http.open.Load())
	require.Equal(t, uint64(1), s.network.http.opened.Load())
	transport.CloseIdleConnections()
	require.Eventually(t, func() bool { return s.network.http.open.Load() == 0 && s.network.http.closed.Load() == 1 }, time.Second, time.Millisecond)
}

func TestConnectionPeakConcurrentLifecycle(t *testing.T) {
	var (
		c       connectionCounters
		workers sync.WaitGroup
	)

	release := make(chan struct{})

	for range 32 {
		workers.Go(func() {
			c.begin()
			<-release
			c.end()
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer workers.Wait()
	defer close(release)

	for (c.open.Load() != 32 || c.peak.Load() != 32) && ctx.Err() == nil {
		time.Sleep(time.Millisecond)
	}

	require.Equal(t, int64(32), c.open.Load())
	require.Equal(t, int64(32), c.peak.Load())
}

func TestCancelledOutboundRPCFinishesAccounting(t *testing.T) {
	var counters rpcCounters

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := counters.clientRPC(ctx, "/godb.peer.v1.Peer/Step", nil, nil, nil,
		func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			require.Equal(t, int64(1), counters.active.Load())
			return ctx.Err()
		})
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, counters.active.Load())
	require.Equal(t, uint64(1), counters.started.Load())
	require.Equal(t, uint64(1), counters.failed.Load())
}
