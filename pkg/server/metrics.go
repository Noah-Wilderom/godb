package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.etcd.io/raft/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/Noah-Wilderom/godb/internal/observe"
	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/replication"
)

type serverMetrics struct {
	sqlAggregates                                                             *prometheus.CounterVec
	sqlSelects                                                                *prometheus.CounterVec
	reads, sqlPrepare, sqlDuration, mysqlCatalog                              *prometheus.HistogramVec
	readPending, sqlActive                                                    prometheus.Gauge
	sqlRows, sqlAffected, sqlMatched, sqlDeleted                              prometheus.Counter
	registry                                                                  *prometheus.Registry
	proposals                                                                 *prometheus.HistogramVec
	peerReceive, peerSend                                                     prometheus.Histogram
	applied                                                                   *prometheus.CounterVec
	peerRejected, leaderChanges, failures, endpointErrors                     prometheus.Counter
	running, pending, term, commit, appliedIndex, leader, hasLeader, progress prometheus.Gauge
	lastStatus                                                                atomic.Pointer[replication.Status]
}

func (s *Server) initMetrics() error {
	m := &serverMetrics{
		mysqlCatalog: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "godb_mysql_catalog_duration_seconds", Help: "Schema discovery catalog reads including quorum and local catalog access, during prepare and execution.", Buckets: prometheus.DefBuckets}, []string{"outcome"}),
		reads:        prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "godb_read_duration_seconds", Help: "Linearizable read caller latency including queue, quorum, apply and local work.", Buckets: prometheus.DefBuckets}, []string{"outcome"}),
		sqlPrepare:   prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "godb_sql_prepare_duration_seconds", Help: "SQL preparation latency including parsing and consistent catalog access.", Buckets: prometheus.DefBuckets}, []string{"outcome"}),
		sqlDuration:  prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "godb_sql_statement_duration_seconds", Help: "Prepared SQL execution latency by bounded statement kind and outcome.", Buckets: prometheus.DefBuckets}, []string{"statement", "outcome"}),
		registry:     prometheus.NewRegistry(),
		proposals:    prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "godb_proposal_duration_seconds", Help: "Local proposal caller latency by bounded outcome, including queue and commit wait.", Buckets: prometheus.DefBuckets}, []string{"outcome"}),
		peerReceive:  observe.Duration("peer_receive", "Peer RPC handling latency including queue wait and local persistence."),
		peerSend:     observe.Duration("peer_send", "Outbound peer RPC latency including connection wait and errors."),
		applied:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "godb_applied_commands_total", Help: "Command outcomes applied after server startup, including duplicate-request entries."}, []string{"result"}),
	}
	counter := func(name, help string) prometheus.Counter {
		return prometheus.NewCounter(prometheus.CounterOpts{Name: "godb_" + name + "_total", Help: help})
	}
	gauge := func(name, help string) prometheus.Gauge {
		return prometheus.NewGauge(prometheus.GaugeOpts{Name: "godb_" + name, Help: help})
	}
	m.peerRejected = counter("peer_receive_errors", "Peer RPC calls returning an error.")
	m.leaderChanges = counter("raft_leader_changes", "Changes to the locally observed leader ID, including transitions to or from unknown.")
	m.failures = counter("server_failures", "Fatal server processing or peer listener failures.")
	m.endpointErrors = counter("metrics_endpoint_errors", "Unexpected metrics HTTP server exits.")
	m.running = gauge("server_running", "One while the local event loop is running; not a quorum or readiness check.")
	m.pending = gauge("proposals_pending", "Local callers currently waiting for a matching applied command.")
	m.term = gauge("raft_term", "Last term observed by the event loop.")
	m.commit = gauge("raft_commit_index", "Last committed index observed by the event loop.")
	m.appliedIndex = gauge("raft_applied_index", "Last applied index observed by the event loop.")
	m.leader = gauge("raft_is_leader", "One when this member last observed itself as leader; not proof of quorum.")
	m.hasLeader = gauge("raft_has_leader", "One when this member last observed a nonzero leader ID.")
	m.progress = gauge("event_loop_last_progress_timestamp_seconds", "Wall clock time of the last completed event-loop iteration.")
	m.readPending = gauge("reads_pending", "Read barriers awaiting quorum or application, including canceled barriers until acknowledged or leadership changes.")
	m.sqlActive = gauge("sql_statements_in_flight", "Prepared SQL executions not yet returned.")
	m.sqlAggregates = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "godb_sql_aggregate_total", Help: "Prepared aggregate SELECT executions by function and outcome, including quorum failures."}, []string{"function", "outcome"})
	m.sqlSelects = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "godb_sql_select_total", Help: "Prepared SELECT executions by access path and outcome; includes quorum failures and NULL short circuits."}, []string{"access", "outcome"})
	m.sqlRows = counter("sql_rows_returned", "Rows returned by successful SQL executions.")
	m.sqlDeleted = counter("sql_rows_deleted", "Rows removed by successful SQL DELETE executions, including successful deduplicated retries.")
	m.sqlMatched = counter("sql_rows_matched", "Rows matched by successful SQL UPDATE executions, including unchanged rows and successful deduplicated retries.")
	m.sqlAffected = counter("sql_rows_affected", "Rows affected by successful SQL executions, including successful deduplicated retries.")
	s.metrics = m
	register := prometheus.WrapRegistererWith(prometheus.Labels{"group": s.config.Identity.GroupID.String(), "node": strconv.FormatUint(s.config.Identity.NodeID, 10)}, m.registry)

	group := observe.Group{
		m.sqlAggregates, m.sqlSelects, m.mysqlCatalog, m.reads, m.readPending, m.sqlPrepare, m.sqlDuration, m.sqlActive, m.sqlRows, m.sqlAffected, m.sqlMatched, m.sqlDeleted,
		observe.Gauge("read_queue_entries", "Buffered read requests not yet processed.", func() float64 { return float64(len(s.reads)) }),
		s.db.Metrics(), s.store.Metrics(), m.proposals, m.peerReceive, m.peerSend, m.applied, m.peerRejected, m.leaderChanges, m.failures, m.endpointErrors, m.running, m.pending, m.term, m.commit, m.appliedIndex, m.leader, m.hasLeader, m.progress,
		observe.Counter("peer_sent", "Successful outbound peer RPCs.", func() float64 { return float64(s.stats.sent.Load()) }),
		observe.Counter("peer_failed", "Failed outbound peer RPCs.", func() float64 { return float64(s.stats.failed.Load()) }),
		observe.Counter("peer_dropped", "Outbound messages dropped before sending.", func() float64 { return float64(s.stats.dropped.Load()) }),
		observe.Gauge("proposal_queue_entries", "Buffered proposals not yet processed.", func() float64 { return float64(len(s.proposals)) }),
		observe.Gauge("peer_receive_queue_entries", "Buffered peer messages not yet processed.", func() float64 { return float64(len(s.inbound)) }),
		observe.Gauge("peer_send_queue_bytes", "Total outstanding serialized peer bytes including in-flight requests.", func() float64 {
			var total int

			for _, p := range s.peers {
				p.mu.Lock()
				total += p.bytes
				p.mu.Unlock()
			}

			return float64(total)
		}),
	}
	if err := register.Register(group); err != nil {
		return err
	}

	if s.mysql != nil {
		if err := register.Register(s.mysql.Metrics()); err != nil {
			return err
		}
	}

	if err := s.registerNetworkMetrics(register); err != nil {
		return err
	}

	if err := m.registry.Register(collectors.NewGoCollector()); err != nil {
		return err
	}

	if err := m.registry.Register(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})); err != nil {
		return err
	}

	state, err := s.node.Status()
	if err != nil {
		return err
	}

	s.observeStatus(state)
	return nil
}

func (s *Server) MetricsAddress() string {
	if s.monitorListener == nil {
		return ""
	}

	return s.monitorListener.Addr().String()
}

func (s *Server) MetricsHandler() http.Handler {
	return promhttp.HandlerFor(s.metrics.registry, promhttp.HandlerOpts{MaxRequestsInFlight: 2, Timeout: 5 * time.Second, EnableOpenMetrics: true})
}

func (s *Server) observeStatus(state replication.Status) {
	m := s.metrics

	previous := m.lastStatus.Swap(&state)
	if previous != nil && previous.LeaderID != state.LeaderID {
		m.leaderChanges.Inc()
	}

	m.term.Set(float64(state.Term))
	m.commit.Set(float64(state.Commit))
	m.appliedIndex.Set(float64(state.Applied))
	m.leader.Set(0)

	if state.IsLeader {
		m.leader.Set(1)
	}

	m.hasLeader.Set(0)

	if state.LeaderID != 0 {
		m.hasLeader.Set(1)
	}

	m.progress.SetToCurrentTime()
}

func proposalOutcome(result replication.Result, err error) string {
	if err == nil {
		return resultCode(result.Command.Code)
	}

	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, replication.ErrNotLeader):
		return "not_leader"
	case errors.Is(err, ErrBusy), errors.Is(err, raft.ErrProposalDropped):
		return "busy"
	case errors.Is(err, ErrRequestPending):
		return "pending"
	case errors.Is(err, replication.ErrInvalidProposal):
		return "invalid"
	case errors.Is(err, ErrClosed):
		return "closed"
	default:
		return "error"
	}
}

func resultCode(code database.CommandResultCode) string {
	switch code {
	case database.CommandOK, database.CommandDuplicatePrimaryKey, database.CommandUniqueConflict, database.CommandTableIDExists, database.CommandTableNameExists, database.CommandTableNotFound, database.CommandSchemaMismatch, database.CommandMutationLimit, database.CommandRequestIDConflict, database.CommandInvalidUpdate:
		return string(code)
	default:
		return "unknown"
	}
}

func finishSpan(span trace.Span, outcome string, failed bool) {
	span.SetAttributes(attribute.String("godb.outcome", outcome))

	if failed {
		span.SetStatus(codes.Error, outcome)
	}

	span.End()
}
