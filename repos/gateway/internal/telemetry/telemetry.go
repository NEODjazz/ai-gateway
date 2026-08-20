package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

type Config struct {
	ServiceName string
	Version     string
	Endpoint    string
	SampleRatio float64
}

type Shutdown func(context.Context) error

func Setup(ctx context.Context, cfg Config) (Shutdown, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	if strings.TrimSpace(cfg.Endpoint) == "" {
		otel.SetTracerProvider(trace.NewNoopTracerProvider())
		return func(context.Context) error { return nil }, nil
	}
	if err := validateEndpoint(cfg.Endpoint); err != nil {
		return nil, err
	}
	if cfg.SampleRatio < 0 || cfg.SampleRatio > 1 {
		return nil, errors.New("OTEL_TRACE_SAMPLE_RATIO must be between 0 and 1")
	}
	if strings.TrimSpace(cfg.ServiceName) == "" {
		cfg.ServiceName = "ai-gateway"
	}
	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(cfg.Endpoint))
	if err != nil {
		return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
	}
	serviceResource := resource.NewSchemaless(
		attribute.String("service.name", cfg.ServiceName),
		attribute.String("service.version", cfg.Version),
	)
	mergedResource, err := resource.Merge(resource.Default(), serviceResource)
	if err != nil {
		_ = exporter.Shutdown(ctx)
		return nil, fmt.Errorf("create telemetry resource: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
		sdktrace.WithResource(mergedResource),
	)
	otel.SetTracerProvider(provider)
	return func(shutdownCtx context.Context) error {
		err := provider.Shutdown(shutdownCtx)
		otel.SetTracerProvider(trace.NewNoopTracerProvider())
		return err
	}, nil
}

func validateEndpoint(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Path == "" || parsed.Path == "/" {
		return errors.New("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT must be an absolute HTTP(S) URL including the traces path")
	}
	return nil
}
