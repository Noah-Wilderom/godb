package server

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/proto"

	peerv1 "github.com/Noah-Wilderom/godb/api/peer/v1"
	"github.com/Noah-Wilderom/godb/internal/observe"
	"github.com/Noah-Wilderom/godb/pkg/replication"
)

const (
	maxPeerQueueBytes    = 8 << 20
	maxPeerQueueMessages = 32
)

type peerSender struct {
	connection *grpc.ClientConn
	client     peerv1.PeerClient
	queue      chan *peerv1.StepRequest
	mu         sync.Mutex
	bytes      int
}

type TransportStats struct {
	Sent    uint64
	Failed  uint64
	Dropped uint64
}

type transportCounters struct {
	sent    atomic.Uint64
	failed  atomic.Uint64
	dropped atomic.Uint64
}

func (s *Server) TransportStats() TransportStats {
	return TransportStats{Sent: s.stats.sent.Load(), Failed: s.stats.failed.Load(), Dropped: s.stats.dropped.Load()}
}

func (s *Server) openPeers(tlsConfig *peerTLS) error {
	for id, address := range s.config.Peers {
		if id == s.config.Identity.NodeID {
			continue
		}

		connection, err := grpc.NewClient("passthrough:///"+address,
			grpc.WithUnaryInterceptor(s.network.outbound.clientRPC),
			grpc.WithStatsHandler(&peerStatsHandler{Handler: otelgrpc.NewClientHandler(otelgrpc.WithTracerProvider(s.config.TracerProvider), otelgrpc.WithMeterProvider(metricnoop.NewMeterProvider()), otelgrpc.WithPropagators(propagation.TraceContext{})), counters: &s.network.outbound}),
			grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig.client(id))),
			grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(maxRPCBytes), grpc.MaxCallRecvMsgSize(1024)),
		)
		if err != nil {
			return err
		}

		s.peers[id] = &peerSender{connection: connection, client: peerv1.NewPeerClient(connection), queue: make(chan *peerv1.StepRequest, maxPeerQueueMessages)}
	}

	return nil
}

func (s *Server) enqueue(message replication.Message) {
	id := message.Raft.GetTo()

	peer := s.peers[id]
	if peer == nil {
		s.stats.dropped.Add(1)
		return
	}

	data, err := proto.Marshal(message.Raft)
	if err != nil {
		s.dropped(id)
		return
	}

	request := &peerv1.StepRequest{GroupId: append([]byte(nil), message.GroupID[:]...), Message: data}
	size := len(data) + len(request.GroupId)
	peer.mu.Lock()
	defer peer.mu.Unlock()

	if size > maxPeerQueueBytes-peer.bytes {
		s.dropped(id)
		return
	}

	select {
	case peer.queue <- request:
		peer.bytes += size
	default:
		s.dropped(id)
	}
}

func (s *Server) dropped(id uint64) {
	s.stats.dropped.Add(1)
	s.reportFailure(id)
}

func (s *Server) reportFailure(id uint64) {
	select {
	case s.failures <- id:
	default:
	}
}

func (s *Server) sendLoop(id uint64, peer *peerSender) {
	for {
		select {
		case <-s.ctx.Done():
			return
		case request := <-peer.queue:
			start := time.Now()
			ctx, cancel := context.WithTimeout(s.ctx, s.config.RPCTimeout)
			_, err := peer.client.Step(ctx, request, grpc.WaitForReady(true))
			cancel()
			observe.Since(s.metrics.peerSend, start)
			peer.mu.Lock()
			peer.bytes -= len(request.Message) + len(request.GroupId)
			peer.mu.Unlock()

			if err != nil {
				s.stats.failed.Add(1)
				s.reportFailure(id)
			} else {
				s.stats.sent.Add(1)
			}
		}
	}
}
