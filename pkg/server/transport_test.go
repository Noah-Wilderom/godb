package server

import (
	"context"
	"crypto/tls"
	"net"
	"os"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/require"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	peerv1 "github.com/Noah-Wilderom/godb/api/peer/v1"
	"github.com/Noah-Wilderom/godb/internal/testutil"
	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/replication"
)

func rpcClient(t testing.TB, address string, config *tls.Config) peerv1.PeerClient {
	t.Helper()
	connection, err := grpc.NewClient("passthrough:///"+address, grpc.WithTransportCredentials(credentials.NewTLS(config)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	return peerv1.NewPeerClient(connection)
}

func heartbeat(t testing.TB, from uint64) *peerv1.StepRequest {
	t.Helper()
	encoded, err := proto.Marshal(&raftpb.Message{Type: raftpb.MsgHeartbeat.Enum(), From: proto.Uint64(from), To: proto.Uint64(1), Term: proto.Uint64(1)})
	require.NoError(t, err)
	group := uuid.UUID{1}
	return &peerv1.StepRequest{GroupId: group[:], Message: encoded}
}

func callPeer(client peerv1.PeerClient, request *peerv1.StepRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := client.Step(ctx, request)
	return err
}

func TestPeerRPCAuthenticatesSenderAndGroup(t *testing.T) {
	c := setupCluster(t, 2)
	c.start(t, 1)
	tlsConfig, err := loadTLS(c.configs[2])
	require.NoError(t, err)
	client := rpcClient(t, c.configs[1].Peers[1], tlsConfig.client(1))
	require.NoError(t, callPeer(client, heartbeat(t, 2)))
	require.Equal(t, codes.PermissionDenied, status.Code(callPeer(client, heartbeat(t, 3))))
	wrongGroup := heartbeat(t, 2)
	wrongGroup.GroupId[0] = 9
	require.Equal(t, codes.InvalidArgument, status.Code(callPeer(client, wrongGroup)))
	bad := heartbeat(t, 2)
	bad.Message = []byte{0xff}
	require.Equal(t, codes.InvalidArgument, status.Code(callPeer(client, bad)))
	bad.Message = make([]byte, maxRPCBytes+1)
	require.Equal(t, codes.ResourceExhausted, status.Code(callPeer(client, bad)))
	require.NoError(t, callPeer(client, heartbeat(t, 2)))
}

func TestPeerTLSRejectsUntrustedAndWrongIdentities(t *testing.T) {
	c := setupCluster(t, 2)
	c.start(t, 1)
	tlsConfig, err := loadTLS(c.configs[2])
	require.NoError(t, err)

	for _, kind := range []string{"no certificate", "unknown member", "other group", "other CA", "wrong server identity"} {
		t.Run(kind, func(t *testing.T) {
			config := tlsConfig.client(1)

			switch kind {
			case "no certificate":
				config.Certificates = nil
			case "unknown member", "other group", "other CA":
				group, id, ca := uuid.UUID{1}, uint64(2), c.ca
				if kind == "unknown member" {
					id = 9
				}

				if kind == "other group" {
					group = uuid.UUID{9}
				}

				if kind == "other CA" {
					ca = testutil.NewAuthority(t)
				}

				certPath, keyPath := ca.Issue(t, group, id)
				cert, err := tls.LoadX509KeyPair(certPath, keyPath)
				require.NoError(t, err)
				config.Certificates = []tls.Certificate{cert}
			case "wrong server identity":
				config = tlsConfig.client(2)
			}

			client := rpcClient(t, c.configs[1].Peers[1], config)
			require.Equal(t, codes.Unavailable, status.Code(callPeer(client, heartbeat(t, 2))))
		})
	}
}

func TestPeerQueueDropsWithoutBlockingAndReportsFailure(t *testing.T) {
	peer := &peerSender{queue: make(chan *peerv1.StepRequest, maxPeerQueueMessages)}
	s := &Server{peers: map[uint64]*peerSender{2: peer}, failures: make(chan uint64, 1)}

	message := replication.Message{GroupID: uuid.UUID{1}, Raft: &raftpb.Message{To: proto.Uint64(2), Type: raftpb.MsgHeartbeat.Enum()}}
	for range maxPeerQueueMessages + 10 {
		s.enqueue(message)
	}

	require.Len(t, peer.queue, maxPeerQueueMessages)
	require.Equal(t, uint64(10), s.TransportStats().Dropped)
	require.Equal(t, uint64(2), <-s.failures)
	peer.queue = make(chan *peerv1.StepRequest, maxPeerQueueMessages)
	peer.bytes = 0
	message.Raft.Entries = []*raftpb.Entry{{Data: make([]byte, maxPeerQueueBytes/2)}}
	s.enqueue(message)
	s.enqueue(message)
	require.Len(t, peer.queue, 1)
	require.LessOrEqual(t, peer.bytes, maxPeerQueueBytes)
	require.Equal(t, uint64(11), s.TransportStats().Dropped)
}

func TestStartRejectsUnsafeInitializationAndUnlocksOnError(t *testing.T) {
	for _, kind := range []string{"missing journal", "nonempty database", "wrong local cert", "membership change"} {
		t.Run(kind, func(t *testing.T) {
			c := setupCluster(t, 1)
			config := c.configs[1]

			switch kind {
			case "missing journal":
				config.Bootstrap = false
			case "nonempty database":
				db, err := database.Open(config.DatabasePath)
				require.NoError(t, err)
				require.NoError(t, db.Put([]byte("key"), []byte("value")))
				require.NoError(t, db.Close())
			case "wrong local cert":
				config.CertFile, config.KeyFile = c.ca.Issue(t, uuid.UUID{1}, 2)
			case "membership change":
				c.start(t, 1)
				require.NoError(t, c.members[1].Close())
				listener, err := net.Listen("tcp", config.Peers[1])
				require.NoError(t, err)
				c.listeners[1] = listener
				config.Peers = map[uint64]string{1: config.Peers[1], 2: "127.0.0.1:1"}
			}

			_, err := Start(context.Background(), config, c.listeners[1])
			require.Error(t, err)

			if kind == "missing journal" || kind == "nonempty database" {
				_, err := os.Stat(config.RaftPath)
				require.ErrorIs(t, err, os.ErrNotExist)
			}

			db, err := database.Open(config.DatabasePath)
			require.NoError(t, err)
			require.NoError(t, db.Close())
		})
	}
}
