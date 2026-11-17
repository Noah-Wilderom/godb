package server

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/stats"

	"github.com/Noah-Wilderom/godb/internal/observe"
)

type connectionCounters struct {
	open, peak     atomic.Int64
	opened, closed atomic.Uint64
}

func (c *connectionCounters) begin() {
	current := c.open.Add(1)
	c.opened.Add(1)

	for previous := c.peak.Load(); current > previous; previous = c.peak.Load() {
		if c.peak.CompareAndSwap(previous, current) {
			break
		}
	}
}

func (c *connectionCounters) end() {
	c.open.Add(-1)
	c.closed.Add(1)
}

type rpcCounters struct {
	connections                     connectionCounters
	active                          atomic.Int64
	started, failed, received, sent atomic.Uint64
}

type networkCounters struct {
	inbound, outbound rpcCounters
	http              connectionCounters
}

type peerStatsHandler struct {
	stats.Handler

	counters *rpcCounters
}

func (h *peerStatsHandler) HandleConn(ctx context.Context, event stats.ConnStats) {
	switch event.(type) {
	case *stats.ConnBegin:
		h.counters.connections.begin()
	case *stats.ConnEnd:
		h.counters.connections.end()
	}

	h.Handler.HandleConn(ctx, event)
}

func (h *peerStatsHandler) HandleRPC(ctx context.Context, event stats.RPCStats) {
	switch event := event.(type) {
	case *stats.InPayload:
		if event.WireLength > 0 {
			h.counters.received.Add(uint64(event.WireLength))
		}
	case *stats.OutPayload:
		if event.WireLength > 0 {
			h.counters.sent.Add(uint64(event.WireLength))
		}
	}

	h.Handler.HandleRPC(ctx, event)
}

func (c *rpcCounters) beginRPC() {
	c.started.Add(1)
	c.active.Add(1)
}

func (c *rpcCounters) endRPC(err error) {
	c.active.Add(-1)

	if err != nil {
		c.failed.Add(1)
	}
}

func (c *rpcCounters) clientRPC(ctx context.Context, method string, request, reply any, connection *grpc.ClientConn, invoke grpc.UnaryInvoker, options ...grpc.CallOption) (err error) {
	c.beginRPC()
	defer func() { c.endRPC(err) }()
	return invoke(ctx, method, request, reply, connection, options...)
}

func (c *rpcCounters) serverRPC(ctx context.Context, request any, _ *grpc.UnaryServerInfo, handle grpc.UnaryHandler) (_ any, err error) {
	c.beginRPC()
	defer func() { c.endRPC(err) }()
	return handle(ctx, request)
}

func (s *Server) observeHTTPConnection(_ net.Conn, state http.ConnState) {
	switch state {
	case http.StateNew:
		s.network.http.begin()
	case http.StateClosed, http.StateHijacked:
		s.network.http.end()
	}
}

func (s *Server) registerNetworkMetrics(register prometheus.Registerer) error {
	for _, entry := range []struct {
		kind        string
		connections *connectionCounters
		rpc         *rpcCounters
	}{
		{"peer_inbound", &s.network.inbound.connections, &s.network.inbound},
		{"peer_outbound", &s.network.outbound.connections, &s.network.outbound},
		{"metrics_http", &s.network.http, nil},
	} {
		c := entry.connections

		group := observe.Group{
			observe.Gauge("connections", "Open gRPC transports or metrics HTTP connections, including idle connections; excludes failed TLS handshakes.", func() float64 { return float64(c.open.Load()) }),
			observe.Gauge("connections_peak", "Highest observed simultaneous open connections since member startup, per kind.", func() float64 { return float64(c.peak.Load()) }),
			observe.Counter("connections_opened", "Established connections since member startup; not attempted dials or TLS failures.", func() float64 { return float64(c.opened.Load()) }),
			observe.Counter("connections_closed", "Established connections closed since member startup.", func() float64 { return float64(c.closed.Load()) }),
		}
		if rpc := entry.rpc; rpc != nil {
			group = append(group,
				observe.Gauge("peer_rpc_in_flight", "RPCs begun but not ended, including outbound connection wait; not connections or SQL sessions.", func() float64 { return float64(rpc.active.Load()) }),
				observe.Counter("peer_rpc_started", "Logical peer RPC calls started; inbound counts decoded calls reaching the handler.", func() float64 { return float64(rpc.started.Load()) }),
				observe.Counter("peer_rpc_errors", "Logical peer RPC calls returning a non-OK status, including cancellation.", func() float64 { return float64(rpc.failed.Load()) }),
				observe.Counter("peer_rpc_received_bytes", "Received gRPC payload bytes including compression/framing, excluding headers and TCP/TLS overhead.", func() float64 { return float64(rpc.received.Load()) }),
				observe.Counter("peer_rpc_sent_bytes", "Sent gRPC payload bytes including compression/framing, excluding headers and TCP/TLS overhead.", func() float64 { return float64(rpc.sent.Load()) }),
			)
		}

		if err := prometheus.WrapRegistererWith(prometheus.Labels{"kind": entry.kind}, register).Register(group); err != nil {
			return err
		}
	}

	return nil
}
