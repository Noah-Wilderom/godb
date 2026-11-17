package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"
	"uuid"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	peerv1 "github.com/Noah-Wilderom/godb/api/peer/v1"
	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/mysqlwire"
	"github.com/Noah-Wilderom/godb/pkg/raftstore"
	"github.com/Noah-Wilderom/godb/pkg/replication"
)

const (
	maxPending  = 128
	maxInbound  = 16
	maxRPCBytes = replication.MaxMessageBytes + 1024
)

type proposal struct {
	ctx     context.Context
	id      uuid.UUID
	command []byte
	digest  [sha256.Size]byte
	reply   chan proposalResult
}

type proposalResult struct {
	result replication.Result
	err    error
}

type inbound struct {
	ctx     context.Context
	message replication.Message
	reply   chan error
}

type Server struct {
	mysql           *mysqlwire.Endpoint
	metrics         *serverMetrics
	tracer          trace.Tracer
	monitor         *http.Server
	monitorListener net.Listener
	config          Config
	node            *replication.Node
	db              *database.Database
	store           *raftstore.Store
	grpc            *grpc.Server
	listener        net.Listener
	ctx             context.Context
	cancel          context.CancelFunc
	done            chan struct{}
	err             error
	proposals       chan *proposal
	reads           chan *readRequest
	readSequence    uint64
	nextSQLTableID  uint64
	inbound         chan inbound
	failures        chan uint64
	peers           map[uint64]*peerSender
	workers         sync.WaitGroup
	stats           transportCounters
	network         networkCounters
}

func Start(ctx context.Context, config Config, listener net.Listener) (_ *Server, err error) {
	if listener == nil {
		return nil, ErrInvalidConfig
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	config, err = config.normalized()
	if err != nil {
		return nil, err
	}

	tlsConfig, err := loadTLS(config)
	if err != nil {
		return nil, err
	}

	s := &Server{config: config, listener: listener, done: make(chan struct{}), proposals: make(chan *proposal, 32), inbound: make(chan inbound, maxInbound), failures: make(chan uint64, len(config.Peers)), peers: make(map[uint64]*peerSender)}
	s.reads = make(chan *readRequest, 32)
	s.nextSQLTableID = 1
	s.tracer = config.TracerProvider.Tracer("github.com/Noah-Wilderom/godb/server")
	defer func() {
		if err != nil {
			err = errors.Join(err, s.closeFiles())
		}
	}()

	if config.MetricsAddress != "" {
		var listenConfig net.ListenConfig

		s.monitorListener, err = listenConfig.Listen(ctx, "tcp", config.MetricsAddress)
		if err != nil {
			return nil, err
		}
	}

	s.db, err = database.Open(config.DatabasePath)
	if err != nil {
		return nil, err
	}

	s.store, err = raftstore.Open(config.RaftPath, config.Identity)
	if errors.Is(err, os.ErrNotExist) && config.Bootstrap {
		if err := requireEmpty(s.db); err != nil {
			return nil, err
		}

		s.store, err = raftstore.Create(config.RaftPath, config.Identity)
	}

	if err != nil {
		return nil, err
	}

	last, err := s.store.LastIndex()
	if err != nil {
		return nil, err
	}

	var bootstrap []uint64

	if last == 0 {
		if !config.Bootstrap {
			return nil, fmt.Errorf("%w: empty journal requires explicit bootstrap", ErrInvalidConfig)
		}

		if err := requireEmpty(s.db); err != nil {
			return nil, err
		}

		bootstrap = config.memberIDs()
	}

	var initial replication.Output

	s.node, initial, err = replication.New(replication.Config{Store: s.store, Database: s.db, BootstrapPeers: bootstrap})
	if err != nil {
		return nil, err
	}

	members, err := s.node.Members()
	if err != nil {
		return nil, err
	}

	if !slices.Equal(members, config.memberIDs()) {
		return nil, fmt.Errorf("%w: peers differ from durable membership", ErrInvalidConfig)
	}

	s.grpc = grpc.NewServer(grpc.UnaryInterceptor(s.network.inbound.serverRPC), grpc.Creds(credentials.NewTLS(tlsConfig.server())), grpc.MaxRecvMsgSize(maxRPCBytes), grpc.MaxSendMsgSize(maxRPCBytes), grpc.MaxConcurrentStreams(maxInbound), grpc.StatsHandler(&peerStatsHandler{Handler: otelgrpc.NewServerHandler(otelgrpc.WithTracerProvider(config.TracerProvider), otelgrpc.WithMeterProvider(metricnoop.NewMeterProvider()), otelgrpc.WithPropagators(propagation.TraceContext{})), counters: &s.network.inbound}))
	peerv1.RegisterPeerServer(s.grpc, &peerService{server: s, slots: make(chan struct{}, maxInbound)})

	if err := s.openPeers(tlsConfig); err != nil {
		return nil, err
	}

	if err := s.openMySQL(ctx, tlsConfig.certificate); err != nil {
		return nil, err
	}

	if err := s.initMetrics(); err != nil {
		return nil, err
	}

	if s.monitorListener != nil {
		mux := http.NewServeMux()
		mux.Handle("GET /metrics", s.MetricsHandler())
		s.monitor = &http.Server{Handler: mux, ConnState: s.observeHTTPConnection, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	}

	s.ctx, s.cancel = context.WithCancel(ctx)
	for id, peer := range s.peers {
		s.workers.Go(func() { s.sendLoop(id, peer) })
	}

	go s.run(initial)
	return s, nil
}

func requireEmpty(db *database.Database) error {
	entries, err := db.Scan(nil, nil, 1)
	if err != nil {
		return err
	}

	if len(entries) != 0 {
		return fmt.Errorf("%w: bootstrap requires an empty database", ErrInvalidConfig)
	}

	return nil
}

func (s *Server) closeFiles() error {
	var err error

	if s.mysql != nil {
		s.mysql.Close()
	}

	if s.monitorListener != nil {
		closeErr := s.monitorListener.Close()
		if !errors.Is(closeErr, net.ErrClosed) {
			err = errors.Join(err, closeErr)
		}
	}

	if s.node != nil {
		s.node.Close()
	}

	for _, peer := range s.peers {
		err = errors.Join(err, peer.connection.Close())
	}

	if s.db != nil {
		err = errors.Join(err, s.db.Close())
	}

	if s.store != nil {
		err = errors.Join(err, s.store.Close())
	}

	return err
}

func (s *Server) Wait() error {
	<-s.done
	return s.err
}

func (s *Server) Close() error {
	s.cancel()
	return s.Wait()
}

func (s *Server) Status() (replication.Status, error) {
	return s.node.Status()
}

func (s *Server) Propose(ctx context.Context, id uuid.UUID, command []byte) (result replication.Result, err error) {
	start := time.Now()
	ctx, span := s.tracer.Start(ctx, "godb.propose")
	defer func() {
		outcome := proposalOutcome(result, err)
		s.metrics.proposals.WithLabelValues(outcome).Observe(time.Since(start).Seconds())
		finishSpan(span, outcome, err != nil)
	}()

	if err := ctx.Err(); err != nil {
		return replication.Result{}, err
	}

	if _, err := replication.EncodeProposal(id, command); err != nil {
		return replication.Result{}, err
	}

	request := &proposal{ctx: ctx, id: id, command: bytes.Clone(command), digest: sha256.Sum256(command), reply: make(chan proposalResult, 1)}
	select {
	case <-s.ctx.Done():
		return replication.Result{}, ErrClosed
	case <-ctx.Done():
		return replication.Result{}, ctx.Err()
	case s.proposals <- request:
	default:
		return replication.Result{}, ErrBusy
	}

	select {
	case result := <-request.reply:
		return result.result, result.err
	case <-ctx.Done():
		return replication.Result{}, ctx.Err()
	case <-s.ctx.Done():
		return replication.Result{}, ErrClosed
	}
}

func (s *Server) run(initial replication.Output) {
	s.metrics.running.Set(1)

	var mysqlResult chan error

	if s.mysql != nil {
		mysqlResult = make(chan error, 1)
		go func() { mysqlResult <- s.mysql.Serve(s.ctx) }()
	}

	var monitorResult chan error

	if s.monitor != nil {
		monitorResult = make(chan error, 1)
		result := monitorResult
		go func() { result <- s.monitor.Serve(s.monitorListener) }()
	}

	serveResult := make(chan error, 1)
	go func() { serveResult <- s.grpc.Serve(s.listener) }()
	ticker := time.NewTicker(s.config.TickInterval)
	defer ticker.Stop()
	pending := make(map[uuid.UUID]*proposal)
	reads := make(map[uint64]*readRequest)
	s.dispatch(initial, pending)

	var (
		fatal         error
		serveFinished bool
	)

loop:
	for {
		var (
			output replication.Output
			err    error
		)

		select {
		case err = <-mysqlResult:
			mysqlResult = nil
			fatal = err
			break loop
		case monitorErr := <-monitorResult:
			if monitorErr != nil && !errors.Is(monitorErr, http.ErrServerClosed) {
				s.metrics.endpointErrors.Inc()
				slog.Error("metrics endpoint stopped", "error", monitorErr)
			}

			monitorResult = nil
		case <-s.ctx.Done():
			break loop
		case err = <-serveResult:
			serveFinished = true

			if !errors.Is(err, grpc.ErrServerStopped) {
				fatal = err
			}

			break loop
		case <-ticker.C:
			output, err = s.node.Tick()
		case id := <-s.failures:
			output, err = s.node.ReportUnreachable(id)
		case request := <-s.reads:
			output, err = s.beginRead(request, reads)
		case request := <-s.inbound:
			if request.ctx.Err() != nil {
				request.reply <- request.ctx.Err()
				continue
			}

			_, span := s.tracer.Start(request.ctx, "godb.raft.step")
			output, err = s.node.Step(request.message)

			outcome := "ok"
			if err != nil {
				outcome = "error"
			}

			finishSpan(span, outcome, err != nil)
			request.reply <- err
		case request := <-s.proposals:
			if request.ctx.Err() != nil {
				request.reply <- proposalResult{err: request.ctx.Err()}
				continue
			}

			if previous := pending[request.id]; previous != nil && previous.ctx.Err() == nil {
				request.reply <- proposalResult{err: ErrRequestPending}
				continue
			}

			delete(pending, request.id)

			if len(pending) >= maxPending {
				request.reply <- proposalResult{err: ErrBusy}
				continue
			}

			pending[request.id] = request

			_, span := s.tracer.Start(request.ctx, "godb.proposal.process")
			output, err = s.node.Propose(request.id, request.command)

			outcome := "ok"
			if err != nil {
				outcome = "error"
			}

			finishSpan(span, outcome, err != nil)

			if err != nil {
				delete(pending, request.id)
				request.reply <- proposalResult{err: err}
			}
		}

		if errors.Is(err, replication.ErrStopped) || errors.Is(err, replication.ErrClosed) {
			fatal = err
			break
		}

		s.dispatch(output, pending)

		state, stateErr := s.node.Status()
		if stateErr != nil {
			fatal = stateErr
			break
		}

		s.observeStatus(state)
		s.completeReads(output.Reads, state, reads)

		for id, request := range pending {
			if request.ctx.Err() != nil {
				delete(pending, id)
				continue
			}

			if !state.IsLeader {
				request.reply <- proposalResult{err: replication.ErrNotLeader}
				delete(pending, id)
			}
		}

		s.metrics.pending.Set(float64(len(pending)))
	}

	if fatal != nil {
		s.metrics.failures.Inc()
	}

	s.metrics.running.Set(0)
	s.metrics.pending.Set(0)
	s.metrics.readPending.Set(0)
	s.cancel()

	if s.mysql != nil {
		s.mysql.Close()

		if mysqlResult != nil {
			fatal = errors.Join(fatal, <-mysqlResult)
		}
	}

	if s.monitor != nil {
		_ = s.monitor.Close()

		if monitorResult != nil {
			monitorErr := <-monitorResult
			_ = monitorErr
		}
	}

	s.grpc.Stop()
	_ = s.listener.Close()

	if !serveFinished {
		serveErr := <-serveResult
		if serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
			fatal = errors.Join(fatal, serveErr)
		}
	}

	s.workers.Wait()
	s.err = errors.Join(fatal, s.closeFiles())
	close(s.done)
}

func (s *Server) dispatch(output replication.Output, pending map[uuid.UUID]*proposal) {
	for _, result := range output.Results {
		if s.metrics != nil {
			s.metrics.applied.WithLabelValues(resultCode(result.Command.Code)).Inc()
		}

		request := pending[result.RequestID]
		if request != nil && request.digest == result.Digest {
			request.reply <- proposalResult{result: result}
			delete(pending, result.RequestID)
		}
	}

	for _, message := range output.Messages {
		s.enqueue(message)
	}
}
