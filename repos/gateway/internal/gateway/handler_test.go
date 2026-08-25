package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type modelsProvider struct{}

type accessPolicyModule struct {
	models []string
	tools  []string
	rpm    int
	tpm    int
}

type countingAccessModule struct{ calls int }

func (m *countingAccessModule) Name() string   { return "counting-access" }
func (m *countingAccessModule) Required() bool { return true }
func (m *countingAccessModule) Handle(context.Context, *modules.RequestContext) error {
	m.calls++
	return nil
}

func (m accessPolicyModule) Name() string   { return "access-policy" }
func (m accessPolicyModule) Required() bool { return true }
func (m accessPolicyModule) Handle(_ context.Context, req *modules.RequestContext) error {
	req.UserID = "user-1"
	req.TeamID = "team-1"
	req.CredentialID = "credential-1"
	req.AllowedModels = append([]string(nil), m.models...)
	req.AllowedTools = append([]string(nil), m.tools...)
	req.RateLimitRPM = m.rpm
	req.RateLimitTPM = m.tpm
	return nil
}

func TestChatToolACLAllowsWildcardAndRejectsUnscopedTool(t *testing.T) {
	llm := &chatProvider{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}, tools: []string{"mcp.weather.*"}}}), llm)
	allowed := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test","messages":[{"role":"user","content":"weather"}],"tools":[{"type":"function","function":{"name":"mcp.weather.forecast"}}]}`))
	allowedResponse := httptest.NewRecorder()
	handler.ChatCompletions(allowedResponse, allowed)
	if allowedResponse.Code != http.StatusOK {
		t.Fatalf("allowed tool rejected: status=%d body=%s", allowedResponse.Code, allowedResponse.Body.String())
	}
	denied := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test","messages":[{"role":"user","content":"mail"}],"tools":[{"type":"function","function":{"name":"mcp.mail.send"}}]}`))
	deniedResponse := httptest.NewRecorder()
	handler.ChatCompletions(deniedResponse, denied)
	if deniedResponse.Code != http.StatusForbidden || !strings.Contains(deniedResponse.Body.String(), "tool_not_allowed") {
		t.Fatalf("unscoped tool accepted: status=%d body=%s", deniedResponse.Code, deniedResponse.Body.String())
	}
}

func TestResponsesMCPACLUsesServerIdentity(t *testing.T) {
	handler := NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}, tools: []string{"mcp:weather-prod@https://mcp.example.test"}}}), &chatProvider{})
	allowed := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test","input":"weather","tools":[{"type":"mcp","server_label":"weather-prod","server_url":"https://mcp.example.test"}]}`))
	allowedResponse := httptest.NewRecorder()
	handler.Responses(allowedResponse, allowed)
	if allowedResponse.Code != http.StatusOK {
		t.Fatalf("allowed MCP server rejected: status=%d body=%s", allowedResponse.Code, allowedResponse.Body.String())
	}
	denied := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test","input":"mail","tools":[{"type":"mcp","server_label":"mail","server_url":"https://mcp.example.test"}]}`))
	deniedResponse := httptest.NewRecorder()
	handler.Responses(deniedResponse, denied)
	if deniedResponse.Code != http.StatusForbidden {
		t.Fatalf("unscoped MCP server accepted: status=%d body=%s", deniedResponse.Code, deniedResponse.Body.String())
	}
}

func TestResponsesMCPACLRejectsLabelReuseForAnotherURL(t *testing.T) {
	handler := NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}, tools: []string{"mcp:weather-prod@https://mcp.example.test"}}}), &chatProvider{})
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test","input":"weather","tools":[{"type":"mcp","server_label":"weather-prod","server_url":"https://evil.example.test"}]}`))
	response := httptest.NewRecorder()
	handler.Responses(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "tool_not_allowed") {
		t.Fatalf("MCP label reuse accepted: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestMCPToolIdentifierRejectsUnsafeURLs(t *testing.T) {
	for _, serverURL := range []string{
		"http://mcp.example.test",
		"https://user@mcp.example.test",
		"https://mcp.example.test?token=secret",
		"https://mcp.example.test#fragment",
	} {
		if identifier, ok := mcpToolIdentifier(openai.ResponseTool{Type: "mcp", ServerLabel: "weather", ServerURL: serverURL}); ok {
			t.Errorf("unsafe MCP URL %q produced identifier %q", serverURL, identifier)
		}
	}
}

func (modelsProvider) ChatCompletions(context.Context, modules.RequestContext) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, nil
}

func (modelsProvider) StreamChatCompletions(context.Context, modules.RequestContext, provider.ChatCompletionStreamWriter) (openai.ChatCompletionResponse, bool, error) {
	return openai.ChatCompletionResponse{}, false, nil
}

func (modelsProvider) Responses(context.Context, modules.RequestContext) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, nil
}

func (modelsProvider) StreamResponses(context.Context, modules.RequestContext, provider.ResponseStreamWriter) (openai.ResponseResponse, bool, error) {
	return openai.ResponseResponse{}, false, nil
}

func (modelsProvider) Models() []openai.Model {
	return []openai.Model{
		{ID: "test-model", Object: "model", OwnedBy: "test-provider"},
	}
}

type chatProvider struct {
	request modules.RequestContext
}

func (p *chatProvider) Embeddings(_ context.Context, req modules.RequestContext) (openai.EmbeddingResponse, error) {
	p.request = req
	return openai.EmbeddingResponse{
		Object: "list", Model: req.EmbeddingRequest.Model,
		Data:  []openai.Embedding{{Object: "embedding", Embedding: []float64{0.1, 0.2}, Index: 0}},
		Usage: openai.Usage{PromptTokens: 2, TotalTokens: 2},
	}, nil
}

func (p *chatProvider) Rerank(_ context.Context, req modules.RequestContext) (openai.RerankResponse, error) {
	p.request = req
	return openai.RerankResponse{ID: "rerank-test", Results: []openai.RerankResult{{Index: 1, RelevanceScore: 0.9}}}, nil
}

func (p *chatProvider) ChatCompletions(_ context.Context, req modules.RequestContext) (openai.ChatCompletionResponse, error) {
	p.request = req
	return openai.ChatCompletionResponse{
		ID:     "chatcmpl-test",
		Object: "chat.completion",
		Model:  req.Request.Model,
		Choices: []openai.Choice{
			{
				Index:        0,
				Message:      openai.Message{Role: "assistant", Content: "hello stream"},
				FinishReason: "stop",
			},
		},
	}, nil
}

func (*chatProvider) StreamChatCompletions(context.Context, modules.RequestContext, provider.ChatCompletionStreamWriter) (openai.ChatCompletionResponse, bool, error) {
	return openai.ChatCompletionResponse{}, false, nil
}

func (*chatProvider) Responses(context.Context, modules.RequestContext) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, nil
}

func (*chatProvider) StreamResponses(context.Context, modules.RequestContext, provider.ResponseStreamWriter) (openai.ResponseResponse, bool, error) {
	return openai.ResponseResponse{}, false, nil
}

func (*chatProvider) Models() []openai.Model {
	return nil
}

func TestEmbeddingsUsesAuthenticatedProviderPipeline(t *testing.T) {
	llm := &chatProvider{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}}}), llm)
	request := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"embed-model","input":["hello","world"]}`))
	request.Header.Set("Authorization", "Bearer test-key")
	response := httptest.NewRecorder()

	handler.Embeddings(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	if llm.request.APIKey != "" || llm.request.EmbeddingRequest == nil {
		t.Fatalf("unsafe or missing provider context: %+v", llm.request)
	}
	if input := openai.EmbeddingInputText(llm.request.EmbeddingRequest.Input); input != "hello\nworld" {
		t.Fatalf("unexpected embedding input: %q", input)
	}
}

func TestRerankUsesAuthenticatedProviderPipeline(t *testing.T) {
	llm := &chatProvider{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}}}), llm)
	request := httptest.NewRequest(http.MethodPost, "/v1/rerank", strings.NewReader(`{"model":"rerank-model","query":"refund","documents":["shipping","refund policy"],"top_n":1}`))
	request.Header.Set("Authorization", "Bearer client-secret")
	response := httptest.NewRecorder()
	handler.Rerank(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	if llm.request.APIKey != "" || llm.request.RerankRequest == nil {
		t.Fatalf("unsafe or missing provider context: %+v", llm.request)
	}
}

func TestRerankRejectsInvalidDocuments(t *testing.T) {
	handler := NewHandler(modules.NewPipeline(nil), &chatProvider{})
	for _, body := range []string{`{"model":"m","query":"q","documents":[]}`, `{"model":"m","query":"q","documents":[{"title":"missing text"}]}`, `{"model":"m","query":"q","documents":["x"],"top_n":2}`} {
		response := httptest.NewRecorder()
		handler.Rerank(response, httptest.NewRequest(http.MethodPost, "/v1/rerank", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
}

func TestEmbeddingsRejectsTokenArraysBeforePipeline(t *testing.T) {
	auth := &countingAccessModule{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{auth}), &chatProvider{})
	request := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"embed-model","input":[1,2,3]}`))
	response := httptest.NewRecorder()

	handler.Embeddings(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	if auth.calls != 0 {
		t.Fatalf("pipeline ran for unsupported token input: %d", auth.calls)
	}
}

type streamingChatProvider struct {
	streamRequest modules.RequestContext
	normalCalled  bool
}

func (p *streamingChatProvider) ChatCompletions(context.Context, modules.RequestContext) (openai.ChatCompletionResponse, error) {
	p.normalCalled = true
	return openai.ChatCompletionResponse{}, nil
}

func (p *streamingChatProvider) StreamChatCompletions(_ context.Context, req modules.RequestContext, write provider.ChatCompletionStreamWriter) (openai.ChatCompletionResponse, bool, error) {
	p.streamRequest = req
	if err := write(`{"id":"chatcmpl-test","object":"chat.completion.chunk","model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","content":"hello live"},"finish_reason":null}]}`); err != nil {
		return openai.ChatCompletionResponse{}, true, err
	}
	return openai.ChatCompletionResponse{
		ID:     "chatcmpl-test",
		Object: "chat.completion",
		Model:  "test-model",
		Choices: []openai.Choice{
			{Index: 0, Message: openai.Message{Role: "assistant", Content: "hello live"}, FinishReason: "stop"},
		},
	}, true, nil
}

func (*streamingChatProvider) Responses(context.Context, modules.RequestContext) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, nil
}

func (*streamingChatProvider) StreamResponses(context.Context, modules.RequestContext, provider.ResponseStreamWriter) (openai.ResponseResponse, bool, error) {
	return openai.ResponseResponse{}, false, nil
}

func (*streamingChatProvider) Models() []openai.Model {
	return nil
}

type streamingResponsesProvider struct {
	streamRequest modules.RequestContext
	normalCalled  bool
}

func (p *streamingResponsesProvider) ChatCompletions(context.Context, modules.RequestContext) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, nil
}

func (*streamingResponsesProvider) StreamChatCompletions(context.Context, modules.RequestContext, provider.ChatCompletionStreamWriter) (openai.ChatCompletionResponse, bool, error) {
	return openai.ChatCompletionResponse{}, false, nil
}

func (p *streamingResponsesProvider) Responses(context.Context, modules.RequestContext) (openai.ResponseResponse, error) {
	p.normalCalled = true
	return openai.ResponseResponse{}, nil
}

func (p *streamingResponsesProvider) StreamResponses(_ context.Context, req modules.RequestContext, write provider.ResponseStreamWriter) (openai.ResponseResponse, bool, error) {
	p.streamRequest = req
	if err := write("response.output_text.delta", `{"type":"response.output_text.delta","response_id":"resp-test","delta":"hello live"}`); err != nil {
		return openai.ResponseResponse{}, true, err
	}
	return openai.ResponseResponse{
		ID:         "resp-test",
		Object:     "response",
		Status:     "completed",
		Model:      req.ResponseRequest.Model,
		OutputText: "hello live",
	}, true, nil
}

func (*streamingResponsesProvider) Models() []openai.Model {
	return nil
}

func TestModelsRequiresAuthorization(t *testing.T) {
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{
		modules.NewAuthModule(true),
	}), modelsProvider{}))

	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d", recorder.Code)
	}
}

func TestModelsReturnsOpenAICompatibleList(t *testing.T) {
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{
		modules.NewAuthModule(true),
	}), modelsProvider{}))

	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer demo-admin-key")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected ok, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var response openai.ModelsResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Object != "list" || len(response.Data) != 1 || response.Data[0].ID != "test-model" {
		t.Fatalf("unexpected models response: %+v", response)
	}
}

func TestRoutesExposePrometheusMetricsAndRequestID(t *testing.T) {
	handler := Routes(NewHandler(modules.NewPipeline(nil), modelsProvider{}))
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || recorder.Header().Get("X-Request-ID") == "" {
		t.Fatalf("missing request observability headers: status=%d headers=%v", recorder.Code, recorder.Header())
	}

	metricsRequest := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRecorder := httptest.NewRecorder()
	handler.ServeHTTP(metricsRecorder, metricsRequest)
	body := metricsRecorder.Body.String()
	if !strings.Contains(body, `ai_gateway_http_requests_total{method="GET",path="/healthz",status="204"} 1`) {
		t.Fatalf("unexpected metrics output: %s", body)
	}
	if strings.Contains(body, `path="/metrics"`) {
		t.Fatalf("metrics endpoint must not observe itself: %s", body)
	}

	unmatchedRequest := httptest.NewRequest(http.MethodGet, "/tenant-controlled-value", nil)
	unmatchedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(unmatchedRecorder, unmatchedRequest)
	metricsRecorder = httptest.NewRecorder()
	handler.ServeHTTP(metricsRecorder, metricsRequest)
	if body := metricsRecorder.Body.String(); !strings.Contains(body, `path="unmatched"`) || strings.Contains(body, "tenant-controlled-value") {
		t.Fatalf("unmatched paths must use a bounded metric label: %s", body)
	}
}

func TestDetailedMetricsExposeOnlyBoundedOperationalLabels(t *testing.T) {
	metrics := NewMetrics()
	metrics.ObserveProvider("azure-a", "openai", "chat", "ok", 10*time.Millisecond)
	metrics.ObserveModule("billing", "pre", "budget_exceeded", 5*time.Millisecond)
	metrics.ObserveModule("dlp", "pre", "content_rejected", 3*time.Millisecond)
	metrics.ObserveCache("get", "hit")
	recorder := httptest.NewRecorder()
	metrics.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	for _, expected := range []string{
		`ai_gateway_provider_attempts_total{endpoint="azure-a",type="openai",operation="chat",result="ok"} 1`,
		`ai_gateway_module_calls_total{module="billing",phase="pre",result="budget_exceeded"} 1`,
		`ai_gateway_billing_events_total{phase="pre",result="budget_exceeded"} 1`,
		`ai_gateway_security_module_calls_total{module="dlp",phase="pre",result="content_rejected"} 1`,
		`ai_gateway_cache_operations_total{operation="get",result="hit"} 1`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing metric %q in:\n%s", expected, body)
		}
	}
}

func TestRoutesContinueIncomingW3CTrace(t *testing.T) {
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	otel.SetTracerProvider(tracerProvider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer func() {
		_ = tracerProvider.Shutdown(context.Background())
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
	}()

	handler := Routes(NewHandler(modules.NewPipeline(nil), modelsProvider{}))
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	spans := spanRecorder.Ended()
	if len(spans) != 1 || spans[0].SpanContext().TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("incoming trace was not continued: spans=%+v", spans)
	}
}

func TestReadinessFailsWithoutLeakingDependencyError(t *testing.T) {
	handler := Routes(NewHandlerWithReadiness(modules.NewPipeline(nil), modelsProvider{}, nil, func(context.Context) error {
		return errors.New("redis password=super-secret")
	}))
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected unavailable readiness, got %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "super-secret") {
		t.Fatalf("readiness response leaked dependency error: %s", recorder.Body.String())
	}
}

func TestProviderBudgetFailureReturns429WithoutDetails(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeProviderFailure(recorder, errors.Join(errors.New("billing internal detail"), modules.ErrBudgetExceeded))
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", recorder.Code)
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"code":"budget_exceeded"`) || strings.Contains(body, "internal detail") {
		t.Fatalf("unexpected budget response: %s", body)
	}
}

func TestProviderAdmissionFailureReturns429WithRetryAfter(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeProviderFailure(recorder, &provider.AdmissionError{Provider: "ollama", RetryAfter: 1500 * time.Millisecond})
	if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") != "2" {
		t.Fatalf("unexpected provider busy response: code=%d retry-after=%q", recorder.Code, recorder.Header().Get("Retry-After"))
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"code":"provider_busy"`) || strings.Contains(body, "ollama") {
		t.Fatalf("provider details leaked in response: %s", body)
	}
}

func TestProviderContentRejectionReturns451WithoutDetails(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeProviderFailure(recorder, errors.Join(errors.New("dlp policy name=secret-policy"), modules.ErrContentRejected))
	if recorder.Code != http.StatusUnavailableForLegalReasons {
		t.Fatalf("expected 451, got %d", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"code":"content_rejected"`) || strings.Contains(body, "secret-policy") {
		t.Fatalf("unexpected content rejection response: %s", body)
	}
}

func TestProviderBillingConflictReturns409(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeProviderFailure(recorder, modules.ErrBillingConflict)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), `"code":"billing_conflict"`) {
		t.Fatalf("unexpected billing conflict response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestModelGrantRejectsRequestBeforeProvider(t *testing.T) {
	provider := &chatProvider{}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{
		accessPolicyModule{models: []string{"allowed-model"}},
	}), provider))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"blocked-model","messages":[{"role":"user","content":"hello"}]}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden || provider.request.Request.Model != "" {
		t.Fatalf("expected grant rejection before provider, status=%d provider_request=%+v", recorder.Code, provider.request)
	}
}

func TestRateLimitRejectsSecondRequest(t *testing.T) {
	provider := &chatProvider{}
	handler := Routes(NewHandlerWithRateLimitStore(modules.NewPipeline([]modules.Module{
		accessPolicyModule{models: []string{"test-model"}, rpm: 1},
	}), provider, NewMemoryRateLimitStore()))

	for attempt := 1; attempt <= 2; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if attempt == 1 && recorder.Code != http.StatusOK {
			t.Fatalf("first request failed: %d %s", recorder.Code, recorder.Body.String())
		}
		if attempt == 2 && (recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") == "") {
			t.Fatalf("second request should be rate-limited: %d %s", recorder.Code, recorder.Body.String())
		}
	}
}

func TestBearerTokenIsClearedBeforeProviderPipeline(t *testing.T) {
	provider := &chatProvider{}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{
		modules.NewAuthModule(true),
	}), provider))

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model": "test-model",
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	request.Header.Set("Authorization", "Bearer demo-admin-key")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected ok, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if provider.request.APIKey != "" {
		t.Fatal("provider pipeline received client bearer token")
	}
	if provider.request.CredentialID == "" {
		t.Fatal("provider pipeline did not receive safe credential fingerprint")
	}
}

func TestChatCompletionsStreamsOpenAICompatibleEvents(t *testing.T) {
	provider := &chatProvider{}
	handler := Routes(NewHandler(modules.NewPipeline(nil), provider))

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model": "test-model",
		"stream": true,
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected ok, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("expected event stream content type, got %q", got)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"object":"chat.completion.chunk"`) {
		t.Fatalf("expected chat completion chunk, got %s", body)
	}
	if !strings.Contains(body, `"content":"hello stream"`) {
		t.Fatalf("expected streamed content, got %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected done event, got %s", body)
	}
	if provider.request.Request.Stream {
		t.Fatal("expected upstream provider request to be non-streaming")
	}
}

func TestChatCompletionsUsesProviderStreamingWhenSupported(t *testing.T) {
	provider := &streamingChatProvider{}
	handler := Routes(NewHandler(modules.NewPipeline(nil), provider))

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model": "test-model",
		"stream": true,
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected ok, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if provider.normalCalled {
		t.Fatal("expected normal chat completions path not to be called")
	}
	if !provider.streamRequest.Request.Stream {
		t.Fatal("expected provider streaming request to keep stream enabled")
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"content":"hello live"`) {
		t.Fatalf("expected live streamed content, got %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected done event, got %s", body)
	}
}

func TestResponsesUsesProviderStreamingWhenSupported(t *testing.T) {
	provider := &streamingResponsesProvider{}
	handler := Routes(NewHandler(modules.NewPipeline(nil), provider))

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model": "test-model",
		"stream": true,
		"input": "hello"
	}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected ok, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if provider.normalCalled {
		t.Fatal("expected normal responses path not to be called")
	}
	if provider.streamRequest.ResponseRequest == nil || !provider.streamRequest.ResponseRequest.Stream {
		t.Fatal("expected provider streaming request to keep stream enabled")
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "event: response.output_text.delta") {
		t.Fatalf("expected response event name, got %s", body)
	}
	if !strings.Contains(body, `"delta":"hello live"`) {
		t.Fatalf("expected live streamed delta, got %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected done event, got %s", body)
	}
}

func TestChatCompletionsAcceptsMultipartMessageContent(t *testing.T) {
	provider := &chatProvider{}
	handler := Routes(NewHandler(modules.NewPipeline(nil), provider))

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model": "test-model",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "describe this"},
					{"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBORw0KGgo="}}
				]
			}
		]
	}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected ok, got %d: %s", recorder.Code, recorder.Body.String())
	}
	content := provider.request.Request.Messages[0].Content
	if _, ok := content.([]any); !ok {
		t.Fatalf("expected multipart content to be preserved, got %T", content)
	}
	if openai.ContentText(content) != "describe this" {
		t.Fatalf("expected text part to be extractable, got %q", openai.ContentText(content))
	}
}

func TestChatCompletionsRejectsRemoteImageURL(t *testing.T) {
	provider := &chatProvider{}
	handler := Routes(NewHandler(modules.NewPipeline(nil), provider))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.test/image.png"}}]}]}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "invalid_image") {
		t.Fatalf("remote image URL accepted: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if provider.request.Request.Model != "" {
		t.Fatal("invalid image reached provider")
	}
}

func TestChatCompletionsRejectsOversizedBody(t *testing.T) {
	handler := Routes(NewHandler(modules.NewPipeline(nil), &chatProvider{}))
	body := strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":"` + strings.Repeat("x", openai.MaxInferenceBodyBytes) + `"}]}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge || !strings.Contains(recorder.Body.String(), "request_too_large") {
		t.Fatalf("oversized request accepted: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
