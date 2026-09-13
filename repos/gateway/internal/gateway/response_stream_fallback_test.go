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

func TestResponsesStreamFallsBackToExplicitNonStreamingDeployment(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		capabilities               []string
		upstreamStatus, wantStatus int
		wantCalls                  int32
	}{
		{"success", []string{"responses"}, 200, 200, 1},
		{"upstream failure", []string{"responses"}, 500, 502, 1},
		{"missing responses capability", []string{"embeddings"}, 200, 502, 0},
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
				fmt.Fprint(w, `{"id":"resp-result","object":"response","status":"completed","model":"m","output":[{"id":"msg","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
			}))
			defer upstream.Close()
			router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "json-only", Type: "openai-compatible", BaseURL: upstream.URL, Models: []string{"m"}, Capabilities: tc.capabilities}}})
			handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"m"}}}}), router))
			request := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","input":"hello","stream":true}`))
			request.Header.Set("Authorization", "Bearer gateway-test-key")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.wantStatus || calls.Load() != tc.wantCalls {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
			}
			if tc.wantStatus == 200 && (!strings.Contains(response.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(response.Body.String(), `"delta":"hello"`) || !strings.Contains(response.Body.String(), "data: [DONE]")) {
				t.Fatalf("invalid SSE: %s", response.Body.String())
			}
		})
	}
}

func TestResponsesStreamOptionsDoNotFallBackToSyntheticStreaming(t *testing.T) {
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "json-only", Type: "openai-compatible", BaseURL: "http://127.0.0.1:1", Models: []string{"m"}, Capabilities: []string{"responses"},
	}}})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"m"}}}}), router))
	request := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","input":"hello","stream":true,"stream_options":{"include_obfuscation":false}}`))
	request.Header.Set("Authorization", "Bearer gateway-test-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), `"streaming_unsupported"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
