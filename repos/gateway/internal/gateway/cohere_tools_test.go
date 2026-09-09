package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
)

func TestCohereToolsPreserveACLTokenReserveAndBilling(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls++
		_, _ = fmt.Fprint(w, `{"id":"chat-tool","finish_reason":"TOOL_CALL","message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}}]},"usage":{"billed_units":{"input_tokens":12,"output_tokens":3}}}`)
	}))
	defer upstream.Close()
	billing := &messagesUsageRecorder{}
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "cohere", Type: "cohere", BaseURL: upstream.URL, Models: []string{"command"}, Capabilities: []string{"chat", "tools"},
	}}, Modules: modules.NewPipeline([]modules.Module{billing})})
	rates := &embeddingTokenRateStore{}
	handler := Routes(NewHandlerWithRateLimitStore(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"command"}, tools: []string{"weather"}, tpm: 1000}}}), router, rates))
	requestBody := `{"model":"command","messages":[{"role":"user","content":"weather"}],"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string","description":"city name used for the lookup"}},"required":["city"]}}}],"tool_choice":"required"}`

	denied := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(strings.Replace(requestBody, `"name":"weather"`, `"name":"denied"`, 1)))
	denied.Header.Set("Authorization", "Bearer gateway-test-key")
	deniedResponse := httptest.NewRecorder()
	handler.ServeHTTP(deniedResponse, denied)
	if deniedResponse.Code != http.StatusForbidden || upstreamCalls != 0 {
		t.Fatalf("tool ACL bypass: %d %s calls=%d", deniedResponse.Code, deniedResponse.Body.String(), upstreamCalls)
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(requestBody))
	request.Header.Set("Authorization", "Bearer gateway-test-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"finish_reason":"tool_calls"`) || upstreamCalls != 1 || rates.tokens <= 10 || billing.calls != 1 || billing.usage.TotalTokens != 15 {
		t.Fatalf("lifecycle mismatch: status=%d upstream=%d reserve=%d billing=%+v body=%s", response.Code, upstreamCalls, rates.tokens, billing, response.Body.String())
	}
}

func TestCohereStreamingToolsPreserveLifecycleAndBilling(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("unexpected accept header: %s", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message-start\ndata: {\"type\":\"message-start\",\"id\":\"chat-tools\",\"delta\":{\"message\":{\"role\":\"assistant\"}}}\n\n"+
			"event: tool-call-start\ndata: {\"type\":\"tool-call-start\",\"index\":3,\"delta\":{\"message\":{\"tool_calls\":{\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"weather\",\"arguments\":\"\"}}}}}\n\n"+
			"event: tool-call-delta\ndata: {\"type\":\"tool-call-delta\",\"index\":3,\"delta\":{\"message\":{\"tool_calls\":{\"function\":{\"arguments\":\"{\\\"city\\\":\\\"Paris\\\"}\"}}}}}\n\n"+
			"event: tool-call-end\ndata: {\"type\":\"tool-call-end\",\"index\":3}\n\n"+
			"event: message-end\ndata: {\"type\":\"message-end\",\"delta\":{\"finish_reason\":\"TOOL_CALL\",\"usage\":{\"billed_units\":{\"input_tokens\":12,\"output_tokens\":3}}}}\n\n")
	}))
	defer upstream.Close()

	billing := &messagesUsageRecorder{}
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "cohere", Type: "cohere", BaseURL: upstream.URL, Models: []string{"command"}, Capabilities: []string{"chat", "stream", "tools"}, Stream: true,
	}}, Modules: modules.NewPipeline([]modules.Module{billing})})
	handler := Routes(NewHandlerWithRateLimitStore(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"command"}, tools: []string{"weather"}, tpm: 100000}}}), router, NewMemoryRateLimitStore()))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"command","messages":[{"role":"user","content":"weather"}],"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}],"tool_choice":"required","stream":true,"stream_options":{"include_usage":true}}`))
	request.Header.Set("Authorization", "Bearer gateway-test-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"tool_calls":[{"index":0`) || !strings.Contains(body, `"finish_reason":"tool_calls"`) || !strings.Contains(body, `"total_tokens":15`) || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("invalid tool stream: status=%d body=%s", response.Code, body)
	}
	if billing.calls != 1 || billing.usage.PromptTokens != 12 || billing.usage.CompletionTokens != 3 || billing.usage.TotalTokens != 15 {
		t.Fatalf("billing mismatch: %+v", billing)
	}
}
