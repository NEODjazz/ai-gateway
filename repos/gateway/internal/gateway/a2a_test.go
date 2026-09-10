package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

type a2aTestProvider struct{ request modules.RequestContext }

func (*a2aTestProvider) ChatCompletions(context.Context, modules.RequestContext) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, nil
}
func (*a2aTestProvider) StreamChatCompletions(context.Context, modules.RequestContext, provider.ChatCompletionStreamWriter) (openai.ChatCompletionResponse, bool, error) {
	return openai.ChatCompletionResponse{}, false, nil
}
func (p *a2aTestProvider) Responses(_ context.Context, request modules.RequestContext) (openai.ResponseResponse, error) {
	p.request = request
	return openai.ResponseResponse{ID: "resp_agent", Model: request.ResponseRequest.Model, Status: "completed", OutputText: "hello from agent"}, nil
}
func (*a2aTestProvider) StreamResponses(context.Context, modules.RequestContext, provider.ResponseStreamWriter) (openai.ResponseResponse, bool, error) {
	return openai.ResponseResponse{}, false, nil
}
func (*a2aTestProvider) Models() []openai.Model {
	return []openai.Model{{ID: "test-model", Object: "model"}}
}

func a2aTestHandler(t *testing.T) (http.Handler, *a2aTestProvider, *lifecycleBillingModule) {
	t.Helper()
	registry := NewAgentRegistry()
	if _, err := registry.PutToolPolicy("safe", ToolPolicy{Name: "Safe", AllowedTools: []string{"weather"}, MaxToolCalls: 2, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.PutAgentProfile("research", AgentProfile{Name: "Research", Description: "Answers questions", Model: "test-model", ToolPolicyID: "safe", MaxIterations: 3, Tags: []string{"research"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	llm := &a2aTestProvider{}
	billing := &lifecycleBillingModule{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: []string{"test-model"}}, billing}), llm).WithAgentRegistry(registry)
	return Routes(handler), llm, billing
}

func TestA2AAgentCardDeclaresOnlyImplementedCapabilities(t *testing.T) {
	router, _, _ := a2aTestHandler(t)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://gateway.example/a2a/research/.well-known/agent-card.json", nil)
	request.Header.Set("X-Forwarded-Proto", "https")
	router.ServeHTTP(response, request)
	body := response.Body.String()
	for _, expected := range []string{`"url":"https://gateway.example/a2a/research"`, `"protocolBinding":"JSONRPC"`, `"protocolVersion":"1.0"`, `"tenant":"research"`, `"streaming":false`, `"pushNotifications":false`, `"httpAuthSecurityScheme"`, `"schemes":{"bearer":{"list":[]}}`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("card missing %s: %s", expected, body)
		}
	}
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || strings.Contains(body, "test-model") || strings.Contains(body, "weather") {
		t.Fatalf("unsafe agent card: status=%d headers=%v body=%s", response.Code, response.Header(), body)
	}
}

func TestA2ASendMessageUsesResponsesPolicyAndBillingPath(t *testing.T) {
	router, llm, billing := a2aTestHandler(t)
	body := `{"jsonrpc":"2.0","id":"rpc-1","method":"SendMessage","params":{"tenant":"research","message":{"messageId":"client-message","contextId":"context-1","role":"ROLE_USER","parts":[{"text":"hello"}]},"configuration":{"acceptedOutputModes":["text/plain"]}}}`
	request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(body))
	request.Header.Set("A2A-Version", "1.0")
	request.Header.Set("Authorization", "Bearer key")
	request.Header.Set("X-Request-ID", "external-correlation")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("X-Execution-ID") == "" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	result := envelope["result"].(map[string]any)
	message := result["message"].(map[string]any)
	if envelope["id"] != "rpc-1" || message["role"] != "ROLE_AGENT" || message["contextId"] != "context-1" || message["messageId"] != "resp_agent" || message["parts"].([]any)[0].(map[string]any)["text"] != "hello from agent" {
		t.Fatalf("unexpected envelope: %v", envelope)
	}
	if llm.request.ResponseRequest == nil || llm.request.ResponseRequest.Model != "test-model" || llm.request.ResponseRequest.User != "" || llm.request.Metadata["gateway.api_type"] != "a2a" || llm.request.RequestID == "external-correlation" {
		t.Fatalf("request bypassed shared execution semantics: %+v", llm.request)
	}
	if billing.calls != 1 {
		t.Fatalf("billing preflight calls=%d", billing.calls)
	}
}

func TestA2ARejectsUnsupportedProtocolFeatures(t *testing.T) {
	router, _, _ := a2aTestHandler(t)
	tests := []struct {
		name, version, body, code string
	}{
		{"version", "0.3", `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{}}`, `"code":-32009`},
		{"method", "1.0", `{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{}}`, `"code":-32601`},
		{"task", "1.0", `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"tenant":"research","message":{"messageId":"m","taskId":"t","role":"ROLE_USER","parts":[{"text":"hello"}]}}}`, `"code":-32004`},
		{"binary", "1.0", `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"tenant":"research","message":{"messageId":"m","role":"ROLE_USER","parts":[{"raw":"AA==","mediaType":"application/octet-stream"}]}}}`, `"code":-32005`},
		{"push", "1.0", `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"tenant":"research","message":{"messageId":"m","role":"ROLE_USER","parts":[{"text":"hello"}]},"configuration":{"pushNotificationConfig":{}}}}`, `"code":-32003`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/a2a/research", strings.NewReader(test.body))
			request.Header.Set("A2A-Version", test.version)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code < 400 || !strings.Contains(response.Body.String(), test.code) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
