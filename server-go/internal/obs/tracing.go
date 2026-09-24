// Package obs wires tracing (OpenTelemetry) and metrics (Prometheus) for the gateway.
package obs

import (
	"context"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// TracerName scopes spans created by gateway code (instrumentation libraries use their own names).
const TracerName = "dovideo/server"

// Tracer returns the gateway tracer from the global provider.
func Tracer() trace.Tracer { return otel.Tracer(TracerName) }

// SetupTracing installs the W3C tracecontext propagator and, when endpoint is non-empty, a batching
// OTLP/gRPC exporter. With an empty endpoint the global provider stays a no-op, so instrumentation
// costs almost nothing and nothing needs to be feature-flagged at call sites. The propagator is
// installed either way: a gateway without an exporter must still forward the caller's traceparent
// to Kafka and gRPC so downstream services keep one trace.
func SetupTracing(ctx context.Context, endpoint, serviceName string) (shutdown func(context.Context) error, err error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	if strings.TrimSpace(endpoint) == "" {
		return func(context.Context) error { return nil }, nil
	}
	opts := []otlptracegrpc.Option{otlptracegrpc.WithTimeout(5 * time.Second)}
	if strings.Contains(endpoint, "://") {
		opts = append(opts, otlptracegrpc.WithEndpointURL(endpoint))
	} else {
		// bare host:port is the compose convention (jaeger:4317) and implies an in-cluster plaintext hop
		opts = append(opts, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	}
	exp, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, err
	}
	// Schemaless: merging with resource.Default() fails when the SDK's semconv schema version differs
	// from the one imported here ("conflicting Schema URL").
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(semconv.ServiceName(serviceName)))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}
