package modules

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type privateErrorModule struct{}

func (privateErrorModule) Name() string   { return "optional-test" }
func (privateErrorModule) Required() bool { return false }
func (privateErrorModule) Handle(context.Context, *RequestContext) error {
	return errors.New("private prompt and credential")
}
func (privateErrorModule) HandleFailure(context.Context, *RequestContext, error) error {
	return errors.New("private prompt and credential")
}

func TestModuleDiagnosticsOmitRawError(t *testing.T) {
	previousTracer := otel.GetTracerProvider()
	spans := tracetest.NewSpanRecorder()
	tracer := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	otel.SetTracerProvider(tracer)
	previousLog := log.Writer()
	var output bytes.Buffer
	log.SetOutput(&output)
	t.Cleanup(func() {
		log.SetOutput(previousLog)
		_ = tracer.Shutdown(context.Background())
		otel.SetTracerProvider(previousTracer)
	})

	pipeline := NewPipeline([]Module{privateErrorModule{}})
	request := &RequestContext{}
	if err := pipeline.Run(t.Context(), request); err != nil {
		t.Fatalf("optional module rejected request: %v", err)
	}
	pipeline.RunFailure(t.Context(), request, errors.New("private prompt and credential"))
	if strings.Contains(output.String(), "private prompt and credential") {
		t.Fatalf("raw module error logged: %s", output.String())
	}
	ended := spans.Ended()
	if len(ended) != 2 {
		t.Fatalf("module spans=%d", len(ended))
	}
	for _, span := range ended {
		if span.Status().Code != codes.Error || span.Status().Description != "error" || len(span.Events()) != 0 {
			t.Fatalf("unsafe module span: %+v", span)
		}
	}
}
