package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modelcatalog"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestDeploymentAndCatalogCapabilityIntersection(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"v1","models":[{"provider":"deployment","model":"upstream","capabilities":["chat","tools"]},{"provider":"empty","model":"upstream","capabilities":[]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name         string
		endpoint     Endpoint
		capabilities []string
		want         bool
	}{
		{name: "deployment excludes tools", endpoint: Endpoint{Name: "deployment", Type: "openai-compatible", ModelAliases: map[string]string{"public": "upstream"}, Capabilities: []string{"chat"}}, capabilities: []string{"chat", "tools"}},
		{name: "alias resolves catalog entry", endpoint: Endpoint{Name: "deployment", Type: "openai-compatible", ModelAliases: map[string]string{"public": "upstream"}, Capabilities: []string{"chat", "tools"}}, capabilities: []string{"chat", "tools"}, want: true},
		{name: "explicit empty catalog denies chat", endpoint: Endpoint{Name: "empty", Type: "openai-compatible", ModelAliases: map[string]string{"public": "upstream"}, Capabilities: []string{"chat"}}, capabilities: []string{"chat"}},
		{name: "unknown model preserves legacy route", endpoint: Endpoint{Name: "legacy", Type: "openai-compatible", Capabilities: []string{"chat"}}, capabilities: []string{"chat"}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := supportsCatalogCapabilities(catalog, test.endpoint, "public", test.capabilities...); got != test.want {
				t.Fatalf("supportsCatalogCapabilities = %t, want %t", got, test.want)
			}
		})
	}
}

func TestMissingDeploymentToolsDoesNotCallUpstream(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "qwen", Type: "openai-compatible", BaseURL: server.URL, Models: []string{"qwen3.6:27b"}, Capabilities: []string{"chat"}}}})
	request := openai.ChatCompletionRequest{Model: "qwen3.6:27b", Tools: []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "lookup", Parameters: map[string]any{"type": "object"}}}}}
	if _, err := router.ChatCompletions(t.Context(), modules.RequestContext{Request: request}); err == nil || !strings.Contains(err.Error(), "required capabilities unavailable") || !strings.Contains(err.Error(), "tools") {
		t.Fatalf("capability mismatch was not reported: %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("upstream calls = %d, want 0", got)
	}
}

func TestMissingOperationCapabilitySkipsUpstream(t *testing.T) {
	for _, test := range []struct {
		name         string
		capabilities []string
		wantError    string
		run          func(context.Context, *Router) (bool, error)
	}{
		{
			name: "chat tools", capabilities: []string{"chat"}, wantError: "tools",
			run: func(ctx context.Context, router *Router) (bool, error) {
				request := openai.ChatCompletionRequest{Model: "public", Tools: []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "lookup", Parameters: map[string]any{"type": "object"}}}}}
				_, err := router.ChatCompletions(ctx, modules.RequestContext{Request: request})
				return false, err
			},
		},
		{
			name: "responses", capabilities: []string{"chat"}, wantError: "responses",
			run: func(ctx context.Context, router *Router) (bool, error) {
				request := openai.ResponseRequest{Model: "public", Input: "hello"}
				_, err := router.Responses(ctx, modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "public"}, ResponseRequest: &request})
				return false, err
			},
		},
		{
			name: "embeddings", capabilities: []string{"chat"}, wantError: "no embedding endpoint",
			run: func(ctx context.Context, router *Router) (bool, error) {
				request := openai.EmbeddingRequest{Model: "public", Input: "hello"}
				_, err := router.Embeddings(ctx, modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "public"}, EmbeddingRequest: &request})
				return false, err
			},
		},
		{
			name: "native chat stream", capabilities: []string{"chat"},
			run: func(ctx context.Context, router *Router) (bool, error) {
				_, handled, err := router.StreamChatCompletions(ctx, modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "public"}}, func(string) error { return nil })
				return handled, err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(upstream.Close)
			router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "deployment", Type: "openai-compatible", BaseURL: upstream.URL, Models: []string{"public"}, Capabilities: test.capabilities}}}).(*Router)
			handled, err := test.run(t.Context(), router)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("capability rejection = %v, want %q", err, test.wantError)
				}
			} else if err != nil || handled {
				// The handler may synthesize SSE from a non-streaming Chat response.
				// This native streaming route must not call the upstream itself.
				t.Fatalf("native stream handled=%t err=%v", handled, err)
			}
			if got := calls.Load(); got != 0 {
				t.Fatalf("upstream calls = %d, want 0", got)
			}
		})
	}
}

func TestCatalogOmissionSkipsAliasedEmbeddingUpstream(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"v1","models":[{"provider":"deployment","model":"upstream","capabilities":["chat"]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(upstream.Close)
	router := New(Config{Catalog: catalog, Endpoints: []config.ProviderEndpointConfig{{
		Name: "deployment", Type: "openai-compatible", BaseURL: upstream.URL,
		Models: []string{"public"}, ModelAliases: map[string]string{"public": "upstream"}, Capabilities: []string{"chat", "embeddings"},
	}}}).(*Router)
	request := openai.EmbeddingRequest{Model: "public", Input: "hello"}
	if _, err := router.Embeddings(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "public"}, EmbeddingRequest: &request}); err == nil || !strings.Contains(err.Error(), "no embedding endpoint") {
		t.Fatalf("missing catalog capability was accepted: %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("upstream calls = %d, want 0", got)
	}
}

func TestNoEndpointErrorDistinguishesModelAndCapability(t *testing.T) {
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "chat-only", Type: "openai-compatible", BaseURL: "http://unused.invalid", Models: []string{"known"}, Capabilities: []string{"chat"}}}})
	response := openai.ResponseRequest{Model: "known", Input: "hello"}
	_, err := router.Responses(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "known"}, ResponseRequest: &response})
	if err == nil || !strings.Contains(err.Error(), "required capabilities unavailable") || !strings.Contains(err.Error(), "responses") {
		t.Fatalf("Responses capability mismatch was not reported: %v", err)
	}
	_, err = router.ChatCompletions(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "missing"}})
	if err == nil || strings.Contains(err.Error(), "required capabilities unavailable") {
		t.Fatalf("unknown model was mislabeled as capability mismatch: %v", err)
	}
}

func TestOtherInferenceRoutesExplainCapabilityMismatch(t *testing.T) {
	for _, test := range []struct {
		name       string
		capability string
		allowed    []string
		run        func(context.Context, *Router, string) error
	}{
		{
			name: "embeddings", capability: "embeddings", allowed: []string{"chat"},
			run: func(ctx context.Context, router *Router, model string) error {
				request := openai.EmbeddingRequest{Model: model, Input: "hello"}
				_, err := router.Embeddings(ctx, modules.RequestContext{Request: openai.ChatCompletionRequest{Model: model}, EmbeddingRequest: &request})
				return err
			},
		},
		{
			name: "rerank", capability: "rerank", allowed: []string{"chat"},
			run: func(ctx context.Context, router *Router, model string) error {
				request := openai.RerankRequest{Model: model, Query: "hello", Documents: []any{"document"}}
				_, err := router.Rerank(ctx, modules.RequestContext{Request: openai.ChatCompletionRequest{Model: model}, RerankRequest: &request})
				return err
			},
		},
		{
			name: "moderation", capability: "moderation", allowed: []string{"chat"},
			run: func(ctx context.Context, router *Router, model string) error {
				request := openai.ModerationRequest{Model: model, Input: "hello"}
				_, err := router.Moderations(ctx, modules.RequestContext{Request: openai.ChatCompletionRequest{Model: model}, ModerationRequest: &request})
				return err
			},
		},
		{
			name: "completions", capability: "chat", allowed: []string{"embeddings"},
			run: func(ctx context.Context, router *Router, model string) error {
				request := openai.CompletionRequest{Model: model, Prompt: "hello"}
				_, err := router.Completions(ctx, modules.RequestContext{Request: openai.ChatCompletionRequest{Model: model, Messages: []openai.Message{{Role: "user", Content: "hello"}}}, CompletionRequest: &request})
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(upstream.Close)
			router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "deployment", Type: "openai-compatible", BaseURL: upstream.URL, Models: []string{"known"}, Capabilities: test.allowed}}}).(*Router)
			if err := test.run(t.Context(), router, "known"); err == nil || !strings.Contains(err.Error(), "required capabilities unavailable") || !strings.Contains(err.Error(), test.capability) {
				t.Fatalf("missing capability was not explained: %v", err)
			}
			if err := test.run(t.Context(), router, "missing"); err == nil || strings.Contains(err.Error(), "required capabilities unavailable") {
				t.Fatalf("unknown model was mislabeled: %v", err)
			}
			if got := calls.Load(); got != 0 {
				t.Fatalf("upstream calls = %d, want 0", got)
			}
		})
	}
}

func TestCatalogCannotExpandDeploymentCapabilities(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"v1","models":[{"provider":"endpoint","model":"m","capabilities":["chat","stream","embeddings","tools"]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := Endpoint{Name: "endpoint", Type: "openai-compatible", Capabilities: []string{"chat"}}
	for _, capability := range []string{"stream", "embeddings", "tools"} {
		if supportsCatalogCapabilities(catalog, endpoint, "m", "chat", capability) {
			t.Fatalf("catalog bypassed deployment capability %s", capability)
		}
	}
	if !supportsCatalogCapabilities(catalog, endpoint, "m", "chat") {
		t.Fatal("supported chat rejected")
	}
	endpoint.Capabilities = nil
	if !supportsCatalogCapabilities(catalog, endpoint, "m", "embeddings") {
		t.Fatal("legacy endpoint lost catalog capability")
	}
}

func TestGeminiManagedToolsRequireExplicitEndpointCapabilities(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"v1","models":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	legacy := Endpoint{Name: "gemini", Type: "gemini", Provider: Gemini{}}
	for _, capability := range []string{"gemini_code_execution", "url_context", "google_maps"} {
		if supportsCatalogCapabilities(catalog, legacy, "model", "chat", capability) {
			t.Fatalf("legacy endpoint implicitly enabled %s", capability)
		}
		explicit := legacy
		explicit.Capabilities = []string{"chat", capability}
		if !supportsCatalogCapabilities(catalog, explicit, "model", "chat", capability) {
			t.Fatalf("explicit endpoint rejected %s", capability)
		}
	}
	if supportsCatalogCapabilities(catalog, Endpoint{Name: "gemini", Type: "gemini", Provider: Gemini{}, Capabilities: []string{"chat", "audio_input", "gemini_audio_timestamp"}}, "model", "chat", "gemini_audio_timestamp") {
		t.Fatal("public Gemini endpoint enabled Vertex-only audio timestamps")
	}
	vertex := Endpoint{Name: "vertex", Type: "vertex-gemini", Provider: NewVertexGemini("https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1/publishers/google", false), Capabilities: []string{"chat", "audio_input", "gemini_audio_timestamp"}}
	if !supportsCatalogCapabilities(catalog, vertex, "model", "chat", "gemini_audio_timestamp") {
		t.Fatal("explicit Vertex audio timestamp capability rejected")
	}
}

func TestGeminiMediaResolutionRequiresExplicitNativeCapability(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"v1","models":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []Endpoint{
		{Name: "gemini", Type: "gemini", Provider: Gemini{}},
		{Name: "vertex", Type: "vertex-gemini", Provider: NewVertexGemini("https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1/publishers/google", false)},
	} {
		if supportsCatalogCapabilities(catalog, endpoint, "model", "chat", "vision", "gemini_media_resolution") {
			t.Fatalf("legacy endpoint implicitly enabled media resolution: %s", endpoint.Type)
		}
		endpoint.Capabilities = []string{"chat", "vision", "gemini_media_resolution"}
		if !supportsCatalogCapabilities(catalog, endpoint, "model", "chat", "vision", "gemini_media_resolution") {
			t.Fatalf("explicit native endpoint rejected media resolution: %s", endpoint.Type)
		}
	}
}

func TestGeminiMediaProcessingRequiresExplicitNativeCapability(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"v1","models":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []Endpoint{
		{Name: "gemini", Type: "gemini", Provider: Gemini{}},
		{Name: "vertex", Type: "vertex-gemini", Provider: NewVertexGemini("https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1/publishers/google", false)},
	} {
		if supportsCatalogCapabilities(catalog, endpoint, "model", "chat", "video_input", "gemini_media_processing") {
			t.Fatalf("legacy endpoint implicitly enabled media processing: %s", endpoint.Type)
		}
		endpoint.Capabilities = []string{"chat", "video_input", "gemini_media_processing"}
		if !supportsCatalogCapabilities(catalog, endpoint, "model", "chat", "video_input", "gemini_media_processing") {
			t.Fatalf("explicit native endpoint rejected media processing: %s", endpoint.Type)
		}
	}
}

func TestGeminiSearchTimeRangeRequiresExplicitNativeCapability(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"v1","models":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []Endpoint{
		{Name: "gemini", Type: "gemini", Provider: Gemini{}},
		{Name: "vertex", Type: "vertex-gemini", Provider: NewVertexGemini("https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1/publishers/google", false)},
	} {
		if supportsCatalogCapabilities(catalog, endpoint, "model", "chat", "web_search", "gemini_search_time_range") {
			t.Fatalf("legacy endpoint implicitly enabled search time range: %s", endpoint.Type)
		}
		endpoint.Capabilities = []string{"chat", "web_search", "gemini_search_time_range"}
		if !supportsCatalogCapabilities(catalog, endpoint, "model", "chat", "web_search", "gemini_search_time_range") {
			t.Fatalf("explicit native endpoint rejected search time range: %s", endpoint.Type)
		}
	}
}

func TestGeminiFileSearchRequiresExplicitNativeCapability(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"v1","models":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []Endpoint{
		{Name: "gemini", Type: "gemini", Provider: Gemini{}},
		{Name: "vertex", Type: "vertex-gemini", Provider: NewVertexGemini("https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1/publishers/google", false)},
	} {
		if supportsCatalogCapabilities(catalog, endpoint, "model", "chat", "gemini_file_search") {
			t.Fatalf("legacy endpoint implicitly enabled file search: %s", endpoint.Type)
		}
		endpoint.Capabilities = []string{"chat", "gemini_file_search"}
		if !supportsCatalogCapabilities(catalog, endpoint, "model", "chat", "gemini_file_search") {
			t.Fatalf("explicit native endpoint rejected file search: %s", endpoint.Type)
		}
	}
}

func TestGeminiComputerUseRequiresExplicitNativeCapability(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"v1","models":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []Endpoint{{Name: "gemini", Type: "gemini", Provider: Gemini{}}, {Name: "vertex", Type: "vertex-gemini", Provider: NewVertexGemini("https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1/publishers/google", false)}} {
		if supportsCatalogCapabilities(catalog, endpoint, "model", "chat", "gemini_computer_use") {
			t.Fatalf("legacy endpoint implicitly enabled computer use: %s", endpoint.Type)
		}
		endpoint.Capabilities = []string{"chat", "gemini_computer_use"}
		if !supportsCatalogCapabilities(catalog, endpoint, "model", "chat", "gemini_computer_use") {
			t.Fatalf("explicit native endpoint rejected computer use: %s", endpoint.Type)
		}
	}
}

func TestGeminiMCPRequiresExplicitNativeCapability(t *testing.T) {
	catalog, err := modelcatalog.Parse(`{"version":"v1","models":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []Endpoint{{Name: "gemini", Type: "gemini", Provider: Gemini{}}, {Name: "vertex", Type: "vertex-gemini", Provider: NewVertexGemini("https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1/publishers/google", false)}} {
		if supportsCatalogCapabilities(catalog, endpoint, "model", "chat", "gemini_mcp") {
			t.Fatalf("legacy endpoint implicitly enabled MCP: %s", endpoint.Type)
		}
		endpoint.Capabilities = []string{"chat", "gemini_mcp"}
		if !supportsCatalogCapabilities(catalog, endpoint, "model", "chat", "gemini_mcp") {
			t.Fatalf("explicit native endpoint rejected MCP: %s", endpoint.Type)
		}
	}
}

func TestRouterSkipsNativeUnsupportedResponseProtocol(t *testing.T) {
	nativeCalls, compatibleCalls := 0, 0
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { nativeCalls++; w.WriteHeader(500) }))
	defer native.Close()
	compatible := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		compatibleCalls++
		_, _ = fmt.Fprint(w, `{"id":"resp-ok","object":"response","model":"m","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}`)
	}))
	defer compatible.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{
		{Name: "native", Type: "gemini", BaseURL: native.URL, Models: []string{"m"}, Capabilities: []string{"responses"}, Priority: 1},
		{Name: "compatible", Type: "openai-compatible", BaseURL: compatible.URL, Models: []string{"m"}, Capabilities: []string{"responses"}, Priority: 2},
	}})
	request := openai.ResponseRequest{Model: "m", Input: "hello"}
	response, err := router.Responses(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "m"}, ResponseRequest: &request})
	if err != nil || response.ID != "resp-ok" || nativeCalls != 0 || compatibleCalls != 1 {
		t.Fatalf("unsupported adapter blocked valid route: native=%d compatible=%d err=%v", nativeCalls, compatibleCalls, err)
	}
}
