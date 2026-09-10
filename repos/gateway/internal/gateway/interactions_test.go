package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

type interactionRequestProvider struct {
	chatProvider
	request *openai.ResponseRequest
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

func TestInteractionsRejectsUnsupportedModesBeforeExecution(t *testing.T) {
	for _, field := range []string{`"stream":true`, `"background":true,"store":false`, `"agent":"research"`, `"generation_config":{"seed":1}`, `"unknown":true`} {
		response := httptest.NewRecorder()
		Handler{}.Interactions(response, httptest.NewRequest(http.MethodPost, "/v1/interactions", strings.NewReader(`{"model":"model","input":"hello",`+field+`}`)))
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_request"`) {
			t.Fatalf("field=%s status=%d body=%s", field, response.Code, response.Body.String())
		}
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
