package gateway

import (
	"encoding/json"
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

func TestChatStreamFallsBackToExplicitNonStreamingDeployment(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		capabilities               []string
		upstreamStatus, wantStatus int
		wantCalls                  int32
	}{
		{"success", []string{"chat"}, 200, 200, 1},
		{"upstream failure", []string{"chat"}, 500, 502, 1},
		{"missing chat capability", []string{"embeddings"}, 200, 502, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body struct {
					Stream bool `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Stream {
					t.Errorf("fallback must send a JSON request: stream=%v err=%v", body.Stream, err)
				}
				if tc.upstreamStatus != 200 {
					w.WriteHeader(tc.upstreamStatus)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"chat-result","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
			}))
			defer upstream.Close()
			router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "json-only", Type: "openai-compatible", BaseURL: upstream.URL, Models: []string{"m"}, Capabilities: tc.capabilities}}})
			handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"m"}}}}), router))
			request := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hello"}],"stream":true}`))
			request.Header.Set("Authorization", "Bearer gateway-test-key")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.wantStatus || calls.Load() != tc.wantCalls {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
			}
			if tc.wantStatus == 200 && (!strings.Contains(response.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(response.Body.String(), `"content":"hello"`) || !strings.Contains(response.Body.String(), "data: [DONE]")) {
				t.Fatalf("invalid SSE: %s", response.Body.String())
			}
		})
	}
}
