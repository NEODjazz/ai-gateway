package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
)

func TestBedrockConverseUsesChatPolicyRoutingAndBilling(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprint(w, `{"id":"chat_1","object":"chat.completion","model":"upstream","choices":[{"index":0,"message":{"role":"assistant","content":"sunny","tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}`)
	}))
	defer upstream.Close()
	recorder := &messagesUsageRecorder{}
	router := provider.New(provider.Config{
		Endpoints: []config.ProviderEndpointConfig{{Name: "deployment", Type: "openai-compatible", BaseURL: upstream.URL, Models: []string{"public"}, ModelAliases: map[string]string{"public": "upstream"}, Capabilities: []string{"chat", "tools"}}},
		Modules:   modules.NewPipeline([]modules.Module{recorder}),
	})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"public"}, tools: []string{"weather"}}}}), router))
	body := `{"messages":[{"role":"user","content":[{"text":"weather"}]}],"toolConfig":{"tools":[{"toolSpec":{"name":"weather","inputSchema":{"json":{"type":"object"}}}}]},"inferenceConfig":{"maxTokens":32}}`
	request := httptest.NewRequest(http.MethodPost, "/model/public/converse?provider=deployment", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer gateway-test-key")
	request.Header.Set("X-Request-ID", "bedrock-converse-external")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("X-Execution-ID") == "" || !strings.Contains(response.Body.String(), `"stopReason":"tool_use"`) || !strings.Contains(response.Body.String(), `"totalTokens":12`) || !strings.Contains(response.Body.String(), `"toolUseId":"call_1"`) {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if calls.Load() != 1 || recorder.calls != 1 || recorder.usage.TotalTokens != 12 {
		t.Fatalf("calls=%d billing_calls=%d usage=%+v", calls.Load(), recorder.calls, recorder.usage)
	}
}

func TestBedrockConverseRejectsUnknownFieldsBeforeExecution(t *testing.T) {
	response := httptest.NewRecorder()
	Routes(Handler{}).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/model/model/converse", strings.NewReader(`{"messages":[{"role":"user","content":[{"text":"hello"}]}],"additionalModelRequestFields":{"top_k":1}}`)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_request"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestBedrockConverseRouteAcceptsEscapedARNModel(t *testing.T) {
	handler := Routes(NewHandler(modules.NewPipeline(nil), &chatProvider{}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/model/arn%3Aaws%3Abedrock%3Aus-east-1%3A123%3Aprompt%2Fprompt-id%3A1/converse", strings.NewReader(`{"messages":[{"role":"user","content":[{"text":"hello"}]}]}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
