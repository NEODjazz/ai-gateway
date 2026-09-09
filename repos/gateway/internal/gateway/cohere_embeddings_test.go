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

func TestCohereEmbeddingEndpointAuthorizationRateLimitAndBilling(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		var request struct {
			Model     string   `json:"model"`
			Texts     []string `json:"texts"`
			InputType string   `json:"input_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/v2/embed" || request.Model != "embed-upstream" || request.InputType != "search_query" || len(request.Texts) != 1 || request.Texts[0] != "one" {
			t.Errorf("native request mismatch: path=%s body=%+v", r.URL.Path, request)
		}
		_, _ = fmt.Fprint(w, `{"embeddings":{"float":[[1,2]]},"meta":{"billed_units":{"input_tokens":4}}}`)
	}))
	defer upstream.Close()

	billing := &embeddingUsageRecorder{}
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "cohere-native", Type: "cohere", BaseURL: upstream.URL, APIKey: "provider-test-key",
		Models: []string{"embed-public"}, ModelAliases: map[string]string{"embed-public": "embed-upstream"}, Capabilities: []string{"embeddings"},
	}}, Modules: modules.NewPipeline([]modules.Module{billing})})
	rates := &embeddingTokenRateStore{}
	handler := Routes(NewHandlerWithRateLimitStore(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"embed-public"}, tpm: 100}}}), router, rates))
	for _, test := range []struct {
		name, body, key string
		status          int
	}{
		{name: "unauthenticated", body: `{"model":"embed-public","input":"one","input_type":"search_query"}`, status: http.StatusUnauthorized},
		{name: "invalid input type", body: `{"model":"embed-public","input":"one","input_type":"image"}`, key: "gateway-test-key", status: http.StatusBadRequest},
		{name: "forbidden model", body: `{"model":"other","input":"one","input_type":"search_query"}`, key: "gateway-test-key", status: http.StatusForbidden},
		{name: "success", body: `{"model":"embed-public","input":"one","input_type":"search_query"}`, key: "gateway-test-key", status: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(test.body))
			if test.key != "" {
				request.Header.Set("Authorization", "Bearer "+test.key)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("expected %d got %d: %s", test.status, response.Code, response.Body.String())
			}
		})
	}
	if upstreamCalls != 1 || rates.tokens <= 0 || billing.pre != 1 || billing.post != 1 || billing.usage.PromptTokens != 4 || billing.usage.TotalTokens != 4 {
		t.Fatalf("lifecycle mismatch: upstream=%d rate_tokens=%d billing=%+v", upstreamCalls, rates.tokens, billing)
	}
}
