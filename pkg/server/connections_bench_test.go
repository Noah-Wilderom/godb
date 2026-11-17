package server

import (
	"context"
	"testing"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc"
	"google.golang.org/grpc/stats"
)

func BenchmarkPeerRPCInstrumentation(b *testing.B) {
	for _, mode := range []string{"OTelOnly", "WithMetrics"} {
		b.Run(mode, func(b *testing.B) {
			counters := &rpcCounters{}

			var handler stats.Handler = otelgrpc.NewServerHandler(otelgrpc.WithTracerProvider(noop.NewTracerProvider()), otelgrpc.WithMeterProvider(metricnoop.NewMeterProvider()))

			if mode == "WithMetrics" {
				handler = &peerStatsHandler{Handler: handler, counters: counters}
			}

			info := &stats.RPCTagInfo{FullMethodName: "/godb.peer.v1.Peer/Step"}
			begin, end := &stats.Begin{}, &stats.End{}
			in, out := &stats.InPayload{WireLength: 128}, &stats.OutPayload{WireLength: 5}

			var process grpc.UnaryHandler = func(ctx context.Context, _ any) (any, error) {
				handler.HandleRPC(ctx, begin)
				handler.HandleRPC(ctx, in)
				handler.HandleRPC(ctx, out)
				handler.HandleRPC(ctx, end)
				return nil, nil
			}

			dispatch := process
			if mode == "WithMetrics" {
				dispatch = func(ctx context.Context, request any) (any, error) {
					return counters.serverRPC(ctx, request, nil, process)
				}
			}

			b.ReportAllocs()

			for b.Loop() {
				ctx := handler.TagRPC(context.Background(), info)
				if _, err := dispatch(ctx, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
