package telemetry

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type TraceConfig struct {
	EndpointURL string
	SampleRatio float64
	InstanceID  string
}

func NewTracerProvider(ctx context.Context, config TraceConfig) (*sdktrace.TracerProvider, error) {
	endpoint, err := url.Parse(config.EndpointURL)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || math.IsNaN(config.SampleRatio) || config.SampleRatio < 0 || config.SampleRatio > 1 {
		return nil, fmt.Errorf("invalid OTLP HTTP endpoint or sampling ratio")
	}

	if endpoint.Path == "" || endpoint.Path == "/" {
		endpoint.Path = "/v1/traces"
	}

	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint.String()), otlptracehttp.WithTimeout(3*time.Second), otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}))
	if err != nil {
		return nil, err
	}

	return sdktrace.NewTracerProvider(
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "godb"), attribute.String("service.instance.id", config.InstanceID))),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(config.SampleRatio))),
		sdktrace.WithBatcher(exporter, sdktrace.WithMaxQueueSize(2048), sdktrace.WithMaxExportBatchSize(512), sdktrace.WithBatchTimeout(time.Second), sdktrace.WithExportTimeout(3*time.Second)),
	), nil
}
