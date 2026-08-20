package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
)

func TestSetupExportsOTLPTraceAndShutsDown(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" || r.Header.Get("Content-Type") != "application/x-protobuf" {
			t.Errorf("unexpected OTLP request: path=%s content-type=%s", r.URL.Path, r.Header.Get("Content-Type"))
		}
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	shutdown, err := Setup(context.Background(), Config{
		ServiceName: "gateway-test", Version: "test", Endpoint: server.URL + "/v1/traces", SampleRatio: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, span := otel.Tracer("test").Start(context.Background(), "test-span")
	span.End()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("expected one OTLP export, got %d", requests.Load())
	}
}

func TestSetupRejectsUnsafeConfiguration(t *testing.T) {
	for _, cfg := range []Config{
		{Endpoint: "collector:4318/v1/traces", SampleRatio: 1},
		{Endpoint: "http://collector:4318", SampleRatio: 1},
		{Endpoint: "http://collector:4318/v1/traces", SampleRatio: 2},
	} {
		if _, err := Setup(context.Background(), cfg); err == nil {
			t.Fatalf("invalid telemetry configuration was accepted: %+v", cfg)
		}
	}
}
