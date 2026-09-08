package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

type embeddingUsageRecorder struct {
	pre, post int
	usage     openai.Usage
}

func (*embeddingUsageRecorder) Name() string              { return "billing" }
func (*embeddingUsageRecorder) Required() bool            { return true }
func (*embeddingUsageRecorder) PostResponseEnabled() bool { return true }
func (m *embeddingUsageRecorder) Handle(_ context.Context, req *modules.RequestContext) error {
	m.pre++
	req.Usage = &openai.Usage{PromptTokens: 999, TotalTokens: 999}
	return nil
}
func (m *embeddingUsageRecorder) HandlePostResponse(_ context.Context, req *modules.RequestContext) error {
	m.post++
	m.usage = req.EmbeddingResponse.Usage
	return nil
}

func TestGeminiEmbeddingEndpointAdmissionAndBilling(t *testing.T) {
	for _, tokens := range []int{0, 12} {
		upstreamCalls := 0
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			upstreamCalls++
			if r.URL.Path != "/v1beta/models/native-embed:batchEmbedContents" || r.Header.Get("x-goog-api-key") != "provider-test-key" {
				t.Error("native routing/auth mismatch")
			}
			_, _ = fmt.Fprintf(w, `{"embeddings":[{"values":[1,2]}],"usageMetadata":{"promptTokenCount":%d}}`, tokens)
		}))
		billing := &embeddingUsageRecorder{}
		router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "native", Type: "gemini", BaseURL: upstream.URL, APIKey: "provider-test-key", Models: []string{"m"}, ModelAliases: map[string]string{"m": "native-embed"}, Capabilities: []string{"embeddings"}}}, Modules: modules.NewPipeline([]modules.Module{billing})})
		handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"m"}, tpm: 100}}}), router))
		for _, tc := range []struct {
			body, key string
			code      int
		}{
			{`{"model":"m","input":"one","dimensions":2}`, "", 401},
			{`{"model":"forbidden","input":"one"}`, "gateway-test-key", 403},
			{`{"model":"m","input":"` + strings.Repeat("large context ", 1000) + `"}`, "gateway-test-key", 429},
			{`{"model":"m","input":"one","user":"unsupported"}`, "gateway-test-key", 400},
			{`{"model":"m","input":"one","dimensions":2}`, "gateway-test-key", 200},
		} {
			req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+tc.key)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tc.code {
				t.Fatalf("expected %d got %d %s", tc.code, response.Code, response.Body.String())
			}
		}
		upstream.Close()
		if upstreamCalls != 1 || billing.pre != 1 || billing.post != 1 || billing.usage.PromptTokens != tokens || billing.usage.TotalTokens != tokens {
			t.Fatalf("billing mismatch: upstream=%d billing=%+v", upstreamCalls, billing)
		}
	}
}
