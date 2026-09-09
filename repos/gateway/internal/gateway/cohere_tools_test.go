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
