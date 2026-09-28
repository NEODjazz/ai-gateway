package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestAttemptMetadataOmitsRawProviderError(t *testing.T) {
	req := modules.RequestContext{Metadata: map[string]string{"provider.error": "stale private prompt"}}
	setAttemptMetadata(&req, time.Now(), &Error{Class: FailureUnavailable, Provider: "private-provider", Err: errors.New("private prompt and credential")})
	if req.Metadata["provider.status"] != "error" || req.Metadata["provider.failure_class"] != string(FailureUnavailable) {
		t.Fatalf("missing bounded failure metadata: %+v", req.Metadata)
	}
	if _, present := req.Metadata["provider.error"]; present {
		t.Fatal("raw provider error remained in request metadata")
	}
}

func TestProviderTraceOmitsRawFailure(t *testing.T) {
	previous := otel.GetTracerProvider()
	spans := tracetest.NewSpanRecorder()
	tracer := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	otel.SetTracerProvider(tracer)
	t.Cleanup(func() {
		_ = tracer.Shutdown(context.Background())
		otel.SetTracerProvider(previous)
	})

	router := Router{}
	_, finish := router.startProviderCall(t.Context(), Endpoint{Name: "deployment", Type: "openai-compatible"}, "chat")
	finish(&Error{Class: FailureUnavailable, Provider: "private-provider", Err: errors.New("private prompt and credential")})
	ended := spans.Ended()
	if len(ended) != 1 || ended[0].Status().Code != codes.Error || ended[0].Status().Description != string(FailureUnavailable) || len(ended[0].Events()) != 0 {
		t.Fatalf("unsafe provider trace: %+v", ended)
	}
}
