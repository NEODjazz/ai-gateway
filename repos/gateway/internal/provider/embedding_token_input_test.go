package provider

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestNativeEmbeddingAdaptersRejectTokenIDsExplicitly(t *testing.T) {
	request := openai.EmbeddingRequest{Model: "embed", Input: []any{1.0, 2.0}}
	assertUnsupportedParameter(t, NewOllama("http://unused.invalid", false).ValidateEmbeddingParameters(request), "input")
	assertUnsupportedParameter(t, NewGemini("http://unused.invalid", "key", false).ValidateEmbeddingParameters(request), "input")
}

func TestRouterRejectsOpaqueEmbeddingTokensWhenDLPIsRequired(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	client := NewOpenAICompatible(server.URL, "", false)
	router := Router{
		endpoints: []Endpoint{{
			Name: "protected", Type: "openai-compatible", Models: []string{"embed"}, Capabilities: []string{"embeddings"},
			DLPEnabled: true, Provider: client, Admission: newAdmissionController(0, 0, 0),
		}},
		modules: modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	request := openai.EmbeddingRequest{Model: "embed", Input: []any{1.0, 2.0}}
	_, err := router.Embeddings(t.Context(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{Model: "embed"}, EmbeddingRequest: &request,
	})
	assertUnsupportedParameter(t, err, "input")
	if calls.Load() != 0 {
		t.Fatalf("opaque tokens reached provider despite DLP policy: %d", calls.Load())
	}
}
