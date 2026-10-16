package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
)

func TestInferenceHTTPExplainsCapabilityMismatch(t *testing.T) {
	for _, test := range []struct {
		name, path, body, capability string
	}{
		{name: "chat", path: "/v1/chat/completions", body: `{"model":"known","messages":[{"role":"user","content":"hello"}]}`, capability: "chat"},
		{name: "responses", path: "/v1/responses", body: `{"model":"known","input":"hello"}`, capability: "responses"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(upstream.Close)
			router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{
				Name: "known-deployment", Type: "openai-compatible", BaseURL: upstream.URL,
				Models: []string{"known"}, Capabilities: []string{"embeddings"},
			}}})
			handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"known"}}}}), router))
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer gateway-test-key")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502: %s", response.Code, response.Body.String())
			}
			var envelope struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != "provider_failed" || !strings.Contains(envelope.Error.Message, "required capabilities unavailable") || !strings.Contains(envelope.Error.Message, test.capability) {
				t.Fatalf("capability mismatch not explained: %+v", envelope.Error)
			}
			if got := calls.Load(); got != 0 {
				t.Fatalf("upstream calls = %d, want 0", got)
			}
		})
	}
}
