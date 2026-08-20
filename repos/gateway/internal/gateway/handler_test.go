package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

type modelsProvider struct{}

type accessPolicyModule struct {
	models []string
	rpm    int
	tpm    int
}

func (m accessPolicyModule) Name() string   { return "access-policy" }
func (m accessPolicyModule) Required() bool { return true }
func (m accessPolicyModule) Handle(_ context.Context, req *modules.RequestContext) error {
	req.UserID = "user-1"
	req.TeamID = "team-1"
	req.CredentialID = "credential-1"
	req.AllowedModels = append([]string(nil), m.models...)
	req.RateLimitRPM = m.rpm
	req.RateLimitTPM = m.tpm
	return nil
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
					{"type": "image_url", "image_url": {"url": "data:image/png;base64,abc"}}
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
