package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
)

func TestCohereChatEndpointAuthorizationRateLimitStreamingAndBilling(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		var request struct {
			Model          string `json:"model"`
			MaxTokens      int    `json:"max_tokens"`
			ResponseFormat *struct {
				Type string `json:"type"`
			} `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/v2/chat" || r.Header.Get("Authorization") != "Bearer provider-test-key" || request.Model != "command-upstream" || request.MaxTokens != 7 || request.ResponseFormat == nil || request.ResponseFormat.Type != "json_object" {
			t.Errorf("native request mismatch: path=%s headers=%v body=%+v", r.URL.Path, r.Header, request)
		}
		_, _ = fmt.Fprintf(w, `{"id":"chat-%d","finish_reason":"COMPLETE","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]},"usage":{"billed_units":{"input_tokens":4,"output_tokens":2}}}`, upstreamCalls)
	}))
	defer upstream.Close()

	billing := &messagesUsageRecorder{}
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "cohere-native", Type: "cohere", BaseURL: upstream.URL, APIKey: "provider-test-key",
		Models: []string{"command-public"}, ModelAliases: map[string]string{"command-public": "command-upstream"}, Capabilities: []string{"chat", "structured_output"},
	}}, Modules: modules.NewPipeline([]modules.Module{billing})})
	rates := NewMemoryRateLimitStore()
	handler := Routes(NewHandlerWithRateLimitStore(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"command-public"}, tpm: 100}}}), router, rates))
	body := `{"model":"command-public","messages":[{"role":"user","content":"hello"}],"max_completion_tokens":7,"response_format":{"type":"json_object"}}`
	bufferedBody := strings.Replace(body, `"max_completion_tokens":7`, `"max_tokens":7`, 1)

	for _, test := range []struct {
		name, requestBody, key string
		status                 int
	}{
		{name: "unauthenticated", requestBody: body, status: http.StatusUnauthorized},
		{name: "forbidden model", requestBody: strings.Replace(body, "command-public", "other", 1), key: "gateway-test-key", status: http.StatusForbidden},
		{name: "over tpm", requestBody: strings.Replace(body, `"max_completion_tokens":7`, `"max_completion_tokens":1000`, 1), key: "gateway-test-key", status: http.StatusTooManyRequests},
		{name: "json", requestBody: body, key: "gateway-test-key", status: http.StatusOK},
		{name: "buffered stream", requestBody: strings.TrimSuffix(bufferedBody, "}") + `,"stream":true,"stream_options":{"include_usage":true}}`, key: "gateway-test-key", status: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(test.requestBody))
			if test.key != "" {
				request.Header.Set("Authorization", "Bearer "+test.key)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("expected %d got %d: %s", test.status, response.Code, response.Body.String())
			}
			if test.name == "buffered stream" && (!strings.Contains(response.Body.String(), `"content":"hello"`) || !strings.Contains(response.Body.String(), `"total_tokens":6`) || !strings.Contains(response.Body.String(), "data: [DONE]")) {
				t.Fatalf("invalid buffered stream: %s", response.Body.String())
			}
		})
	}
	if upstreamCalls != 2 || billing.calls != 2 || billing.usage.PromptTokens != 4 || billing.usage.CompletionTokens != 2 || billing.usage.TotalTokens != 6 {
		t.Fatalf("lifecycle mismatch: upstream=%d billing=%+v", upstreamCalls, billing)
	}
}
