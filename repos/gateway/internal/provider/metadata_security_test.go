package provider

import (
	"context"
	"errors"
	"log"
	"strings"
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

func TestBackgroundJobMetadataRejectsRawProviderError(t *testing.T) {
	metadata := backgroundJobMetadata(map[string]string{
		"provider.error":         "private prompt and credential",
		"provider.status":        "error",
		"policy.example.enabled": "true",
	})
	if _, present := metadata["provider.error"]; present {
		t.Fatal("background job persisted raw provider error")
	}
	if metadata["provider.status"] != "error" || metadata["policy.example.enabled"] != "true" {
		t.Fatalf("safe job metadata lost: %+v", metadata)
	}
}

type failingBackgroundProcessor struct{}

func (failingBackgroundProcessor) ProcessBackgroundResponses(context.Context) (int, error) {
	return 0, errors.New("private prompt and credential")
}

type providerLogLines chan string

func (lines providerLogLines) Write(payload []byte) (int, error) {
	select {
	case lines <- string(payload):
	default:
	}
	return len(payload), nil
}

func TestBackgroundWorkerLogOmitsRawError(t *testing.T) {
	previous := log.Writer()
	lines := make(providerLogLines, 1000)
	log.SetOutput(lines)
	t.Cleanup(func() { log.SetOutput(previous) })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		RunBackgroundResponseWorker(ctx, failingBackgroundProcessor{})
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	deadline := time.After(2 * time.Second)
	for {
		select {
		case line := <-lines:
			if !strings.Contains(line, "background response processing failed") {
				continue
			}
			if strings.Contains(line, "private prompt") || !strings.Contains(line, string(FailureUnknown)) {
				t.Fatalf("unsafe background worker log: %s", line)
			}
			return
		case <-deadline:
			t.Fatal("background worker did not report its failure")
		}
	}
}
