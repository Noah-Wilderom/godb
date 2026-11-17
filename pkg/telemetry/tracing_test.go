package telemetry

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestOTLPTraceExportAndResource(t *testing.T) {
	requests := make(chan *collector.ExportTraceServiceRequest, 1)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/traces" {
			t.Errorf("unexpected export: %s %s", r.Method, r.URL.Path)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}

		request := &collector.ExportTraceServiceRequest{}
		if err := proto.Unmarshal(body, request); err != nil {
			t.Error(err)
			return
		}

		requests <- request
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer endpoint.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	provider, err := NewTracerProvider(ctx, TraceConfig{EndpointURL: endpoint.URL, SampleRatio: 1, InstanceID: "group/node"})
	require.NoError(t, err)
	_, span := provider.Tracer("test").Start(ctx, "godb.test")
	span.End()
	require.NoError(t, provider.Shutdown(ctx))
	select {
	case request := <-requests:
		require.Len(t, request.ResourceSpans, 1)
		resource := request.ResourceSpans[0]

		values := make(map[string]string)
		for _, value := range resource.Resource.Attributes {
			values[value.Key] = value.Value.GetStringValue()
		}

		require.Equal(t, "godb", values["service.name"])
		require.Equal(t, "group/node", values["service.instance.id"])
		require.Equal(t, "godb.test", resource.ScopeSpans[0].Spans[0].Name)
	case <-ctx.Done():
		t.Fatal("no trace export")
	}
}

func TestTraceConfigurationAndZeroSampling(t *testing.T) {
	for _, config := range []TraceConfig{
		{EndpointURL: ""},
		{EndpointURL: "ftp://localhost"},
		{EndpointURL: "https://user:secret@localhost"},
		{EndpointURL: "http://localhost", SampleRatio: -1},
		{EndpointURL: "http://localhost", SampleRatio: 2},
		{EndpointURL: "http://localhost", SampleRatio: math.NaN()},
	} {
		_, err := NewTracerProvider(context.Background(), config)
		require.Error(t, err)
	}

	var requests atomic.Int64

	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.WriteHeader(http.StatusOK) }))
	defer endpoint.Close()
	provider, err := NewTracerProvider(context.Background(), TraceConfig{EndpointURL: endpoint.URL, SampleRatio: 0})
	require.NoError(t, err)
	_, span := provider.Tracer("test").Start(context.Background(), "unsampled")
	span.End()
	require.NoError(t, provider.Shutdown(context.Background()))
	require.Zero(t, requests.Load())
}
