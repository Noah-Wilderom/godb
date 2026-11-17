package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/metadata"

	"github.com/Noah-Wilderom/godb/pkg/database"
	"github.com/Noah-Wilderom/godb/pkg/telemetry"
)

func TestServerMetricsEndpointAndIsolation(t *testing.T) {
	c := setupCluster(t, 1)
	config := c.configs[1]
	config.MetricsAddress = "127.0.0.1:0"
	c.configs[1] = config
	c.start(t, 1)
	c.leader(t)
	s := c.members[1]
	propose(t, s, uuid.UUID{9}, putCommand(t, "sensitive-key", "sensitive-value"))
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get("http://" + s.MetricsAddress() + "/metrics")
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusOK, response.StatusCode)

	for _, name := range []string{"godb_database_writes_total", "godb_wal_sync_duration_seconds", "godb_raft_store_bytes", "godb_proposal_duration_seconds_count", "godb_raft_applied_index", "go_goroutines", "process_cpu_seconds_total"} {
		require.Contains(t, string(body), name)
	}

	require.NotContains(t, string(body), "sensitive-key")
	require.NotContains(t, string(body), "sensitive-value")
	require.NotContains(t, string(body), uuid.UUID{9}.String())
	require.Equal(t, float64(1), testutil.ToFloat64(s.metrics.applied.WithLabelValues(string(database.CommandOK))))
	other := setupCluster(t, 1)
	other.start(t, 1)
	require.Zero(t, testutil.ToFloat64(other.members[1].metrics.applied.WithLabelValues(string(database.CommandOK))))
	require.NoError(t, s.Close())
	require.Zero(t, testutil.ToFloat64(s.metrics.running))
	_, err = client.Get("http://" + s.MetricsAddress() + "/metrics")
	require.Error(t, err)
}

func TestSlowTraceExporterDoesNotBlockProposals(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})

	var once sync.Once

	unblock := func() { once.Do(func() { close(release) }) }
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}

		select {
		case <-release:
		case <-r.Context().Done():
		}

		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer endpoint.Close()
	defer unblock()
	provider, err := telemetry.NewTracerProvider(context.Background(), telemetry.TraceConfig{EndpointURL: endpoint.URL, SampleRatio: 1, InstanceID: "test"})
	require.NoError(t, err)
	defer func() {
		unblock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, provider.Shutdown(ctx))
	}()
	c := setupCluster(t, 1)
	config := c.configs[1]
	config.TracerProvider = provider
	c.configs[1] = config
	c.start(t, 1)
	defer func() { require.NoError(t, c.members[1].Close()) }()
	c.leader(t)
	propose(t, c.members[1], uuid.UUID{1}, putCommand(t, "key", "before"))
	flushed := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		flushed <- provider.ForceFlush(ctx)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("export did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := c.members[1].Propose(ctx, uuid.UUID{2}, putCommand(t, "key", "after"))
	require.NoError(t, err)
	require.Equal(t, database.CommandOK, result.Command.Code)
	unblock()
	require.NoError(t, <-flushed)
}

func TestPeerTraceContextPropagation(t *testing.T) {
	c := setupCluster(t, 2)
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	config := c.configs[1]
	config.TracerProvider = provider
	c.configs[1] = config
	c.start(t, 1)
	tlsConfig, err := loadTLS(c.configs[2])
	require.NoError(t, err)
	client := rpcClient(t, c.configs[1].Peers[1], tlsConfig.client(1))
	ctx, parent := provider.Tracer("test").Start(context.Background(), "peer caller")
	sc := parent.SpanContext()
	ctx = metadata.AppendToOutgoingContext(ctx, "traceparent", fmt.Sprintf("00-%s-%s-01", sc.TraceID(), sc.SpanID()))
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, err = client.Step(ctx, heartbeat(t, 2))
	require.NoError(t, err)
	parent.End()

	var rpcFound, processingFound bool

	for _, span := range exporter.GetSpans() {
		if span.SpanKind == trace.SpanKindServer {
			rpcFound = true
			require.Equal(t, sc.SpanID(), span.Parent.SpanID())
			require.Equal(t, sc.TraceID(), span.SpanContext.TraceID())
		}

		if span.Name == "godb.raft.step" {
			processingFound = true
			require.Equal(t, sc.TraceID(), span.SpanContext.TraceID())
		}
	}

	require.True(t, rpcFound)
	require.True(t, processingFound)
}

func TestMetricsEndpointFailureDoesNotStopDatabase(t *testing.T) {
	c := setupCluster(t, 1)
	config := c.configs[1]
	config.MetricsAddress = "127.0.0.1:0"
	c.configs[1] = config
	c.start(t, 1)
	c.leader(t)
	s := c.members[1]
	require.NoError(t, s.monitorListener.Close())
	require.Eventually(t, func() bool { return testutil.ToFloat64(s.metrics.endpointErrors) == 1 }, time.Second, time.Millisecond)
	propose(t, s, uuid.UUID{3}, putCommand(t, "key", "value"))
	waitValue(t, s, "key", "value")
}

func TestConcurrentMetricsScrapes(t *testing.T) {
	c := setupCluster(t, 1)
	c.start(t, 1)
	s := c.members[1]
	before := s.metrics.lastStatus.Load()
	require.NotNil(t, before)

	var workers sync.WaitGroup

	for range 4 {
		workers.Go(func() {
			for range 20 {
				response := httptest.NewRecorder()
				s.MetricsHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))

				if response.Code != http.StatusOK {
					t.Errorf("scrape status: %d", response.Code)
					return
				}
			}
		})
	}

	workers.Wait()
}

func TestProposalTracingPreservesParentAndOmitsPayload(t *testing.T) {
	c := setupCluster(t, 1)
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	config := c.configs[1]
	config.TracerProvider = provider
	c.configs[1] = config
	c.start(t, 1)
	c.leader(t)
	ctx, parent := provider.Tracer("test").Start(context.Background(), "caller")
	id := uuid.UUID{9}
	_, err := c.members[1].Propose(ctx, id, putCommand(t, "trace-secret-key", "trace-secret-value"))
	require.NoError(t, err)
	parent.End()
	spans := exporter.GetSpans()

	var proposalFound, processingFound bool

	for _, span := range spans {
		if span.Name == "godb.propose" {
			proposalFound = true
			require.Equal(t, parent.SpanContext().SpanID(), span.Parent.SpanID())
			require.Equal(t, parent.SpanContext().TraceID(), span.SpanContext.TraceID())
		}

		if span.Name == "godb.proposal.process" {
			processingFound = true
			require.Equal(t, parent.SpanContext().TraceID(), span.SpanContext.TraceID())
		}

		for _, value := range span.Attributes {
			require.NotContains(t, value.Value.String(), "trace-secret")
			require.NotContains(t, value.Value.String(), id.String())

			if value.Key == attribute.Key("godb.outcome") {
				require.Equal(t, "ok", value.Value.AsString())
			}
		}
	}

	require.True(t, proposalFound)
	require.True(t, processingFound)
	require.NoError(t, c.members[1].Close())
	_, span := provider.Tracer("test").Start(context.Background(), "still caller owned")
	span.End()
	require.True(t, strings.Contains(exporter.GetSpans()[len(exporter.GetSpans())-1].Name, "caller owned"))
}
