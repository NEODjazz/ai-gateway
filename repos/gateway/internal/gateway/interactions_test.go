package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

type interactionRequestProvider struct {
	chatProvider
	request *openai.ResponseRequest
}

type bufferedInteractionProvider struct{ chatProvider }

type interactionOwnershipStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (s *interactionOwnershipStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.data[key]
	return append([]byte(nil), value...), found, nil
}

func (s *interactionOwnershipStore) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = append([]byte(nil), value...)
	return nil
}

func (s *interactionOwnershipStore) SetIfAbsentOrEqual(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, found := s.data[key]; found {
		return string(existing) == string(value), nil
	}
	s.data[key] = append([]byte(nil), value...)
	return true, nil
}

func (s *interactionOwnershipStore) DeleteIfEqual(_ context.Context, key string, value []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, found := s.data[key]
	if !found {
		return true, nil
	}
	if string(existing) != string(value) {
		return false, nil
	}
	delete(s.data, key)
	return true, nil
}

func (*bufferedInteractionProvider) Responses(_ context.Context, request modules.RequestContext) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{
		ID: "interaction_buffered", Object: "response", Model: request.Request.Model, Status: "completed",
		Output: []openai.ResponseOutputItem{{ID: "message", Type: "message", Role: "assistant", Content: []openai.ResponseOutputContent{{Type: "refusal", Refusal: "declined"}}}},
		Usage:  openai.ResponseUsage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3},
	}, nil
}

func (p *interactionRequestProvider) Responses(_ context.Context, request modules.RequestContext) (openai.ResponseResponse, error) {
	p.request = request.ResponseRequest
	return openai.ResponseResponse{ID: "interaction_queued", Model: request.Request.Model, Status: "queued"}, nil
}

func TestInteractionsUsesResponsesPolicyRoutingAndBilling(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body["model"] != "upstream" || body["instructions"] != "be concise" || body["previous_response_id"] != "resp_previous" || body["max_output_tokens"] != float64(32) || body["store"] != false || len(body["tools"].([]any)) != 1 {
			t.Errorf("mapped request=%#v", body)
		}
		text, _ := body["text"].(map[string]any)
		if text["format"] == nil {
			t.Errorf("response format lost: %#v", text)
		}
		_, _ = fmt.Fprint(w, `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"upstream","output":[{"id":"call","type":"function_call","call_id":"call_1","name":"weather","arguments":"{\"city\":\"Paris\"}"},{"id":"message","type":"message","role":"assistant","content":[{"type":"output_text","text":"sunny"}]}],"usage":{"input_tokens":7,"output_tokens":5,"total_tokens":12,"input_tokens_details":{"cached_tokens":2},"output_tokens_details":{"reasoning_tokens":3}}}`)
	}))
	defer upstream.Close()
	recorder := &statelessUsageRecorder{}
	router := provider.New(provider.Config{
		Endpoints: []config.ProviderEndpointConfig{{Name: "deployment", Type: "openai-compatible", BaseURL: upstream.URL, Models: []string{"public"}, ModelAliases: map[string]string{"public": "upstream"}, Capabilities: []string{"responses", "tools", "structured_output"}}},
		Modules:   modules.NewPipeline([]modules.Module{recorder}),
	})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"public"}, tools: []string{"weather"}}}}), router))
	body := `{"provider":"deployment","model":"public","input":"hello","system_instruction":"be concise","previous_interaction_id":"resp_previous","store":false,"tools":[{"type":"function","name":"weather","parameters":{"type":"object"}}],"response_format":{"type":"json_schema","name":"answer","schema":{"type":"object"}},"generation_config":{"max_output_tokens":32}}`
	request := httptest.NewRequest(http.MethodPost, "/v1/interactions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer gateway-test-key")
	request.Header.Set("X-Request-ID", "external-correlation")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("X-Execution-ID") == "" || !strings.Contains(response.Body.String(), `"object":"interaction"`) || !strings.Contains(response.Body.String(), `"type":"function_call"`) || !strings.Contains(response.Body.String(), `"text":"sunny"`) || !strings.Contains(response.Body.String(), `"total_tokens":12`) {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if calls.Load() != 1 || len(recorder.totals) != 1 || recorder.totals[0] != 12 || recorder.ids[0] == "" {
		t.Fatalf("calls=%d ids=%v totals=%v", calls.Load(), recorder.ids, recorder.totals)
	}
}

func TestInteractionsUsesNativeGeminiRoutingAndBilling(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["model"] != "upstream" || body["input"] != "hello" {
			t.Errorf("body=%#v", body)
		}
		generation, _ := body["generation_config"].(map[string]any)
		if generation["seed"] != float64(7) || generation["thinking_level"] != "high" || len(generation["stop_sequences"].([]any)) != 1 {
			t.Errorf("generation=%#v", generation)
		}
		_, _ = fmt.Fprint(w, `{"id":"interaction_native","object":"interaction","model":"upstream","status":"completed","steps":[{"id":"message","type":"model_output","content":[{"type":"text","text":"native"}]}],"usage":{"total_input_tokens":4,"total_output_tokens":2,"total_tokens":6}}`)
	}))
	defer upstream.Close()
	recorder := &statelessUsageRecorder{}
	router := provider.New(provider.Config{
		Endpoints: []config.ProviderEndpointConfig{{Name: "gemini-deployment", Type: "gemini", BaseURL: upstream.URL, APIKey: "secret", Models: []string{"public"}, ModelAliases: map[string]string{"public": "upstream"}, Capabilities: []string{"interactions"}}},
		Modules:   modules.NewPipeline([]modules.Module{recorder}),
	})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"public"}, tpm: 100}}}), router))
	request := httptest.NewRequest(http.MethodPost, "/v1/interactions", strings.NewReader(`{"provider":"gemini-deployment","model":"public","input":"hello","generation_config":{"max_output_tokens":8,"seed":7,"stop_sequences":["END"],"thinking_level":"high"}}`))
	request.Header.Set("Authorization", "Bearer gateway-test-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || calls.Load() != 1 || !strings.Contains(response.Body.String(), `"id":"interaction_native"`) || !strings.Contains(response.Body.String(), `"text":"native"`) || len(recorder.totals) != 1 || recorder.totals[0] != 6 {
		t.Fatalf("status=%d calls=%d totals=%v body=%s", response.Code, calls.Load(), recorder.totals, response.Body.String())
	}
	for _, unsupported := range []string{
		`{"provider":"gemini-deployment","model":"public","input":"hello","previous_interaction_id":"interaction_previous"}`,
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/interactions", strings.NewReader(unsupported))
		request.Header.Set("Authorization", "Bearer gateway-test-key")
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"unsupported_operation"`) {
			t.Fatalf("request=%s status=%d body=%s", unsupported, response.Code, response.Body.String())
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("unsupported native interaction reached provider: calls=%d", calls.Load())
	}
}

func TestInteractionsStreamsNativeGeminiAndSettlesBeforeCompletion(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("alt") != "sse" {
			t.Errorf("query=%s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"interaction_stream\",\"object\":\"interaction\",\"model\":\"upstream\",\"status\":\"in_progress\"}}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"text\":\"native\"}}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"interaction_stream\",\"object\":\"interaction\",\"model\":\"upstream\",\"status\":\"completed\",\"steps\":[{\"id\":\"step_1\",\"type\":\"model_output\",\"content\":[{\"type\":\"text\",\"text\":\"native\"}]}],\"usage\":{\"total_input_tokens\":4,\"total_output_tokens\":2,\"total_tokens\":6}}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	recorder := &statelessUsageRecorder{}
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "gemini-deployment", Type: "gemini", BaseURL: upstream.URL, APIKey: "secret", Models: []string{"public"}, ModelAliases: map[string]string{"public": "upstream"}, Stream: true, Capabilities: []string{"interactions", "stream"}}}, Modules: modules.NewPipeline([]modules.Module{recorder})})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"public"}, tpm: 100}}}), router))
	request := httptest.NewRequest(http.MethodPost, "/v1/interactions", strings.NewReader(`{"provider":"gemini-deployment","model":"public","input":"hello","stream":true,"generation_config":{"max_output_tokens":8}}`))
	request.Header.Set("Authorization", "Bearer gateway-test-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	for _, expected := range []string{"event: interaction.created", "event: step.delta", "event: interaction.completed", `"total_tokens":6`, "event: done\ndata: [DONE]"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %q in %s", expected, body)
		}
	}
	if response.Code != http.StatusOK || len(recorder.totals) != 1 || recorder.totals[0] != 6 {
		t.Fatalf("status=%d totals=%v body=%s", response.Code, recorder.totals, body)
	}
}

func TestNativeInteractionHTTPStoredLifecycleSkipsRepeatBilling(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1beta/interactions":
			_, _ = fmt.Fprint(w, `{"id":"interaction_owned","object":"interaction","model":"upstream","status":"completed","usage":{"total_input_tokens":1,"total_output_tokens":1,"total_tokens":2}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta/interactions/interaction_owned":
			_, _ = fmt.Fprint(w, `{"id":"interaction_owned","object":"interaction","model":"upstream","status":"completed","usage":{"total_tokens":2}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1beta/interactions/interaction_owned:cancel":
			_, _ = fmt.Fprint(w, `{"id":"interaction_owned","status":"cancelled"}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1beta/interactions/interaction_owned":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	recorder := &statelessUsageRecorder{}
	sessions := &interactionOwnershipStore{data: map[string][]byte{}}
	router := provider.New(provider.Config{
		Endpoints:            []config.ProviderEndpointConfig{{Name: "gemini", Type: "gemini", BaseURL: upstream.URL, APIKey: "secret", Models: []string{"public"}, ModelAliases: map[string]string{"public": "upstream"}, Capabilities: []string{"interactions"}}},
		Modules:              modules.NewPipeline([]modules.Module{recorder}),
		SessionStore:         sessions,
		ResponseOwnershipTTL: time.Hour,
	})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: []string{"public"}}}), router))
	call := func(method, path, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer gateway-test-key")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if response := call(http.MethodPost, "/v1/interactions", `{"provider":"gemini","model":"public","input":"hello","store":true,"generation_config":{"max_output_tokens":8}}`); response.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	if response := call(http.MethodGet, "/v1/interactions/interaction_owned", ""); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"model":"public"`) {
		t.Fatalf("retrieve status=%d body=%s", response.Code, response.Body.String())
	}
	if response := call(http.MethodPost, "/v1/interactions/interaction_owned/cancel", ""); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("cancel status=%d body=%s", response.Code, response.Body.String())
	}
	if response := call(http.MethodDelete, "/v1/interactions/interaction_owned", ""); response.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", response.Code, response.Body.String())
	}
	if len(recorder.totals) != 1 || recorder.totals[0] != 2 {
		t.Fatalf("lifecycle produced billing events: %v", recorder.totals)
	}
}

func TestInteractionsRejectsUnsupportedModesBeforeExecution(t *testing.T) {
	for _, field := range []string{`"stream":true,"background":true`, `"background":true,"store":false`, `"agent":"research"`, `"generation_config":{"seed":1}`, `"unknown":true`} {
		response := httptest.NewRecorder()
		Handler{}.Interactions(response, httptest.NewRequest(http.MethodPost, "/v1/interactions", strings.NewReader(`{"model":"model","input":"hello",`+field+`}`)))
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_request"`) {
			t.Fatalf("field=%s status=%d body=%s", field, response.Code, response.Body.String())
		}
	}
}

func TestInteractionsStreamsIncrementalStepsAndSettlesUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_stream\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"upstream\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"message\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n")
		_, _ = fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"item_id\":\"message\",\"delta\":\"hello\"}\n\n")
		_, _ = fmt.Fprint(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"message\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}}\n\n")
		_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_stream\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"upstream\",\"output\":[{\"id\":\"message\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n")
	}))
	defer upstream.Close()
	recorder := &statelessUsageRecorder{}
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "deployment", Type: "openai-compatible", BaseURL: upstream.URL, Models: []string{"public"}, ModelAliases: map[string]string{"public": "upstream"}, Stream: true, Capabilities: []string{"responses", "stream"}}}, Modules: modules.NewPipeline([]modules.Module{recorder})})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"public"}}}}), router))
	request := httptest.NewRequest(http.MethodPost, "/v1/interactions", strings.NewReader(`{"provider":"deployment","model":"public","input":"hello","stream":true}`))
	request.Header.Set("Authorization", "Bearer gateway-test-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	for _, expected := range []string{"event: interaction.created", "event: step.start", `"type":"model_output"`, "event: step.delta", `"text":"hello"`, "event: step.stop", "event: interaction.completed", `"total_tokens":3`, "event: done\ndata: [DONE]"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %q in stream: %s", expected, body)
		}
	}
	if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), "text/event-stream") || strings.Contains(body, "response.output_text") || len(recorder.totals) != 1 || recorder.totals[0] != 3 {
		t.Fatalf("status=%d headers=%v totals=%v body=%s", response.Code, response.Header(), recorder.totals, body)
	}
}

func TestInteractionsSynthesizesStreamForBufferedProvider(t *testing.T) {
	handler := NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"model"}}}), &bufferedInteractionProvider{})
	request := httptest.NewRequest(http.MethodPost, "/v1/interactions", strings.NewReader(`{"model":"model","input":"hello","stream":true}`))
	response := httptest.NewRecorder()
	handler.Interactions(response, request)
	body := response.Body.String()
	for _, expected := range []string{"event: interaction.created", "event: step.start", `"type":"model_output"`, "event: step.delta", `"text":"declined"`, "event: step.stop", "event: interaction.completed", `"total_tokens":3`, "event: done\ndata: [DONE]"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %q in stream: %s", expected, body)
		}
	}
	if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), body)
	}
}

func TestInteractionsPassesDurableBackgroundRequestToExecution(t *testing.T) {
	provider := &interactionRequestProvider{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"model"}}}), provider)
	request := httptest.NewRequest(http.MethodPost, "/v1/interactions", strings.NewReader(`{"model":"model","input":"hello","background":true}`))
	response := httptest.NewRecorder()
	handler.Interactions(response, request)
	if response.Code != http.StatusOK || provider.request == nil || !provider.request.Background || provider.request.Store == nil || !*provider.request.Store {
		t.Fatalf("status=%d request=%+v body=%s", response.Code, provider.request, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"object":"interaction"`) || !strings.Contains(response.Body.String(), `"status":"queued"`) {
		t.Fatalf("unexpected interaction response: %s", response.Body.String())
	}
}

func TestInteractionLifecycleUsesResponseOwnershipWithoutBilling(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		path      string
		status    int
		body      string
		callCount func(*lifecycleResourceProvider) int
	}{
		{name: "retrieve", method: http.MethodGet, path: "/v1/interactions/resp_123", status: http.StatusOK, body: `"object":"interaction"`, callCount: func(p *lifecycleResourceProvider) int { return p.retrieveCalls }},
		{name: "cancel", method: http.MethodPost, path: "/v1/interactions/resp_123/cancel", status: http.StatusOK, body: `"status":"cancelled"`, callCount: func(p *lifecycleResourceProvider) int { return p.cancelCalls }},
		{name: "delete", method: http.MethodDelete, path: "/v1/interactions/resp_123", status: http.StatusNoContent, body: "", callCount: func(p *lifecycleResourceProvider) int { return p.deleteCalls }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resource := &lifecycleResourceProvider{}
			billing := &lifecycleBillingModule{}
			handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{modules.NewAuthModule(true), billing}), resource))
			request := httptest.NewRequest(test.method, test.path, nil)
			request.Header.Set("Authorization", "Bearer demo-user-key")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status || resource.resolveCalls != 1 || test.callCount(resource) != 1 || billing.calls != 0 || response.Body.String() != test.body && !strings.Contains(response.Body.String(), test.body) {
				t.Fatalf("status=%d resolve=%d calls=%d billing=%d body=%q", response.Code, resource.resolveCalls, test.callCount(resource), billing.calls, response.Body.String())
			}
			if response.Header().Get("X-Execution-ID") == "" {
				t.Fatalf("missing execution ID: headers=%v", response.Header())
			}
		})
	}
}
