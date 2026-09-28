package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/batchstate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type privateBatchErrorProvider struct{ *batchProvider }

func (*privateBatchErrorProvider) ChatCompletions(context.Context, modules.RequestContext) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, errors.New("private prompt and credential")
}

func TestHTTPLogFingerprintsExternalRequestID(t *testing.T) {
	previous := log.Writer()
	var output bytes.Buffer
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	const externalID = "private prompt and credential"
	spans := tracetest.NewSpanRecorder()
	tracer := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	t.Cleanup(func() { _ = tracer.Shutdown(context.Background()) })
	ctx, span := tracer.Tracer("test").Start(t.Context(), "request")
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil).WithContext(ctx)
	request.Header.Set("X-Request-ID", externalID)
	response := httptest.NewRecorder()
	observabilityMiddleware(NewMetrics(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(response, request)
	span.End()
	if response.Header().Get("X-Request-ID") != externalID {
		t.Fatalf("external request ID changed: %q", response.Header().Get("X-Request-ID"))
	}
	want := sha256.Sum256([]byte(externalID))
	logged := output.String()
	if strings.Contains(logged, externalID) || !strings.Contains(logged, "sha256:"+fmt.Sprintf("%x", want)) {
		t.Fatalf("unsafe or uncorrelatable HTTP log: %s", logged)
	}
	ended := spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("trace spans=%d", len(ended))
	}
	foundID := false
	for _, field := range ended[0].Attributes() {
		if field.Key != "ai.request.id" {
			continue
		}
		foundID = true
		if field.Value.AsString() != "sha256:"+fmt.Sprintf("%x", want) {
			t.Fatalf("unsafe request ID trace attribute: %s", field.Value.AsString())
		}
	}
	if !foundID {
		t.Fatal("trace omitted request correlation fingerprint")
	}
}

func TestBatchFailureLogOmitsRawErrorAndCustomID(t *testing.T) {
	previous := log.Writer()
	var output bytes.Buffer
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	handler := NewHandler(modules.NewPipeline(nil), &privateBatchErrorProvider{batchProvider: &batchProvider{}})
	item := batchstate.Item{CustomID: "private prompt and credential", ExecutionID: "execution", Identity: json.RawMessage(`{}`), URL: "/v1/chat/completions", Body: json.RawMessage(`{"model":"model","messages":[{"role":"user","content":"hello"}]}`)}
	result, completed, limited := handler.executeBatchItem(t.Context(), batchstate.Batch{ID: "batch-private-credential", Total: 1}, item)
	if !completed || limited || !bytes.Contains(result, []byte(`"code":"provider_error"`)) {
		t.Fatalf("batch result=%s completed=%t limited=%t", result, completed, limited)
	}
	if logged := output.String(); strings.Contains(logged, "private prompt") || strings.Contains(logged, "credential") || !strings.Contains(logged, "batch item") {
		t.Fatalf("unsafe batch failure log: %s", logged)
	}
}
