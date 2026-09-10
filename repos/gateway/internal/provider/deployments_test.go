package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/openai"
)

func TestModelDeploymentUpdateChangesRuntimeCandidates(t *testing.T) {
	enabled := true
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "primary", Type: "demo", Models: []string{"old"}, Enabled: &enabled, Priority: 10, Weight: 1}}}).(*Router)
	list := router.ListModelDeployments(context.Background())
	if len(list) != 1 || !list[0].Enabled || list[0].ProviderType != "demo" {
		t.Fatalf("unsafe or incomplete deployments: %+v", list)
	}
	saved, err := router.UpdateModelDeployment("primary", ModelDeployment{Models: []string{"new"}, Capabilities: []string{"chat"}, Priority: 1, Weight: 5, Enabled: true})
	if err != nil || saved.ProviderType != "demo" {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	endpoints := router.runtimeEndpoints()
	if len(endpoints) != 1 || endpoints[0].Models[0] != "new" || endpoints[0].Weight != 5 {
		t.Fatalf("runtime override not applied: %+v", endpoints)
	}
	_, err = router.UpdateModelDeployment("primary", ModelDeployment{Models: []string{"new"}, Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if endpoints := router.runtimeEndpoints(); len(endpoints) != 0 {
		t.Fatalf("disabled deployment remained routable: %+v", endpoints)
	}
	if diagnostics := router.Diagnostics(context.Background()); len(diagnostics.Endpoints) != 0 {
		t.Fatalf("disabled deployment leaked into active diagnostics: %+v", diagnostics)
	}
}

func TestDeploymentCapabilitiesAcceptImageOperations(t *testing.T) {
	if !validDeploymentCapabilities([]string{"image_generation", "image_edit", "image_variation"}) {
		t.Fatal("image capabilities were rejected")
	}
}

func TestManagedOpenRouterUsesCompatibleAdapter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer router-key" {
			t.Fatalf("request path=%s authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = fmt.Fprint(w, `{"id":"chat","model":"upstream","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()
	router := New(Config{CredentialEncryptionKey: []byte("managed-openrouter-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "router", Type: "openrouter", BaseURL: server.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "router-key", ProviderID: "router", Secret: "router-key"}); err != nil {
		t.Fatal(err)
	}
	endpoint, err := router.endpointForDeployment(ModelDeployment{ProviderID: "router", CredentialID: "router-key", UpstreamModel: "upstream", Models: []string{"public"}, Capabilities: []string{"chat"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	response, err := endpoint.Provider.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "upstream", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
	if err != nil || openai.ContentText(response.Choices[0].Message.Content) != "ok" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestManagedDeploymentRejectsUnsupportedProviderCapabilities(t *testing.T) {
	tests := []struct {
		providerType string
		capability   string
	}{
		{providerType: "voyage", capability: "responses"},
		{providerType: "voyage", capability: "chat"},
		{providerType: "cohere", capability: "responses"},
		{providerType: "cohere", capability: "image_generation"},
		{providerType: "gemini", capability: "responses"},
		{providerType: "gemini", capability: "moderation"},
		{providerType: "mistral", capability: "image_generation"},
		{providerType: "anthropic", capability: "embeddings"},
		{providerType: "ollama", capability: "rerank"},
		{providerType: "demo", capability: "stream"},
		{providerType: "gemini", capability: "web_fetch"},
		{providerType: "demo", capability: "tools"},
		{providerType: "voyage", capability: "structured_output"},
		{providerType: "cohere", capability: "vision"},
		{providerType: "anthropic", capability: "mcp"},
		{providerType: "mistral", capability: "web_search"},
		{providerType: "ollama", capability: "prompt_cache"},
		{providerType: "openai", capability: "prompt_cache"},
		{providerType: "openai", capability: "assistant_prefill"},
	}
	for _, test := range tests {
		t.Run(test.providerType+"/"+test.capability, func(t *testing.T) {
			router := New(Config{}).(*Router)
			if _, err := router.CreateProvider(ManagedProvider{ID: "provider", Type: test.providerType, BaseURL: "https://provider.example", Enabled: true}); err != nil {
				t.Fatal(err)
			}
			capabilities := []string{test.capability}
			switch test.capability {
			case "stream", "tools", "structured_output", "vision", "web_search", "web_fetch", "audio", "prompt_cache", "assistant_prefill":
				capabilities = append([]string{"chat"}, capabilities...)
			case "mcp":
				capabilities = []string{"responses", "tools", "mcp"}
			}
			_, err := router.CreateModelDeployment(ModelDeployment{ID: "deployment", ProviderID: "provider", Models: []string{"model"}, Capabilities: capabilities, Enabled: true})
			if !errors.Is(err, ErrUnsupportedProviderCapability) {
				t.Fatalf("capability %q accepted for %s: %v", test.capability, test.providerType, err)
			}
		})
	}
}

func TestDeploymentCapabilitiesRequireRoutableBaseOperations(t *testing.T) {
	tests := [][]string{
		{"stream"},
		{"tools"},
		{"structured_output"},
		{"vision"},
		{"mcp"},
		{"responses", "mcp"},
		{"chat", "mcp", "tools"},
		{"web_search"},
		{"responses", "web_fetch"},
		{"responses", "audio"},
		{"responses", "prompt_cache"},
		{"responses", "assistant_prefill"},
	}
	for _, capabilities := range tests {
		if validDeploymentCapabilities(capabilities) {
			t.Fatalf("unroutable capabilities accepted: %v", capabilities)
		}
	}
	for _, capabilities := range [][]string{
		nil,
		{},
		{"chat"},
		{"responses", "stream"},
		{"chat", "tools", "structured_output", "vision"},
		{"responses", "tools", "mcp"},
		{"chat", "web_search", "web_fetch", "audio", "prompt_cache", "assistant_prefill"},
		{"embeddings"},
	} {
		if !validDeploymentCapabilities(capabilities) {
			t.Fatalf("routable capabilities rejected: %v", capabilities)
		}
	}
}

func TestModelDeploymentUpdatePreservesOmittedCapabilities(t *testing.T) {
	router := New(Config{}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "provider", Type: "demo", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateModelDeployment(ModelDeployment{ID: "deployment", ProviderID: "provider", Models: []string{"model"}, Capabilities: []string{"chat"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	updated, err := router.UpdateModelDeployment("deployment", ModelDeployment{Models: []string{"model"}, Weight: 1, Enabled: false})
	if err != nil || !slices.Equal(updated.Capabilities, []string{"chat"}) {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
}

func TestManagedDeploymentAcceptsSupportedNativeCapabilities(t *testing.T) {
	router := New(Config{}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "voyage", Type: "voyage", BaseURL: "https://provider.example", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateModelDeployment(ModelDeployment{ID: "voyage-embed", ProviderID: "voyage", Models: []string{"model"}, Capabilities: []string{"embeddings", "rerank"}, Enabled: true}); err != nil {
		t.Fatalf("supported capabilities rejected: %v", err)
	}
}

func TestManagedDeploymentAcceptsSupportedFeatureCapabilities(t *testing.T) {
	tests := []struct {
		providerType string
		capabilities []string
	}{
		{providerType: "ollama", capabilities: []string{"chat", "tools", "structured_output", "vision"}},
		{providerType: "anthropic", capabilities: []string{"chat", "tools", "structured_output", "vision", "web_search", "web_fetch", "prompt_cache", "assistant_prefill"}},
		{providerType: "gemini", capabilities: []string{"chat", "tools", "structured_output", "vision"}},
		{providerType: "cohere", capabilities: []string{"chat", "tools", "structured_output"}},
		{providerType: "mistral", capabilities: []string{"chat", "tools", "structured_output", "vision", "assistant_prefill"}},
		{providerType: "openai-compatible", capabilities: []string{"chat", "responses", "tools", "structured_output", "mcp", "vision", "web_search", "audio"}},
	}
	for _, test := range tests {
		t.Run(test.providerType, func(t *testing.T) {
			router := New(Config{}).(*Router)
			if _, err := router.CreateProvider(ManagedProvider{ID: "provider", Type: test.providerType, BaseURL: "https://provider.example", Enabled: true}); err != nil {
				t.Fatal(err)
			}
			if _, err := router.CreateModelDeployment(ModelDeployment{ID: "deployment", ProviderID: "provider", Models: []string{"model"}, Capabilities: test.capabilities, Enabled: true}); err != nil {
				t.Fatalf("supported capabilities rejected: %v", err)
			}
		})
	}
}

func TestManagedProviderCapabilityProfilesMatchAdapterOperations(t *testing.T) {
	profiles := ManagedProviderCapabilityProfiles()
	byType := make(map[string][]string, len(profiles))
	for _, profile := range profiles {
		byType[profile.Type] = profile.Operations
	}
	if got := byType["voyage"]; !slices.Equal(got, []string{"embeddings", "rerank"}) {
		t.Fatalf("voyage operations=%v", got)
	}
	for _, operation := range []string{"chat", "responses", "embeddings", "rerank", "moderation", "image_generation", "image_edit", "image_variation", "audio_transcription", "audio_speech", "search", "stream"} {
		if !slices.Contains(byType["openai-compatible"], operation) {
			t.Fatalf("openai-compatible missing %s: %v", operation, byType["openai-compatible"])
		}
	}
	if slices.Contains(byType["anthropic"], "embeddings") || !slices.Contains(byType["anthropic"], "responses") {
		t.Fatalf("anthropic operations=%v", byType["anthropic"])
	}
	profilesByType := make(map[string]ProviderCapabilityProfile, len(profiles))
	for _, profile := range profiles {
		profilesByType[profile.Type] = profile
	}
	if !slices.Contains(profilesByType["anthropic"].Capabilities, "web_fetch") || slices.Contains(profilesByType["gemini"].Capabilities, "web_fetch") {
		t.Fatalf("feature profiles are unsafe: anthropic=%v gemini=%v", profilesByType["anthropic"].Capabilities, profilesByType["gemini"].Capabilities)
	}
	if slices.Contains(profilesByType["openai"].Capabilities, "assistant_prefill") || !slices.Contains(profilesByType["mistral"].Capabilities, "assistant_prefill") {
		t.Fatalf("assistant prefill profiles are incorrect: openai=%v mistral=%v", profilesByType["openai"].Capabilities, profilesByType["mistral"].Capabilities)
	}
}

func TestManagedDeploymentEnablesNativeStreaming(t *testing.T) {
	var streamRequested atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		streamRequested.Store(body.Stream)
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"native\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	router := New(Config{}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "native", Type: "openai-compatible", BaseURL: server.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []bool{false, true} {
		capabilities := []string{"chat"}
		if stream {
			capabilities = append(capabilities, "stream")
		}
		endpoint, err := router.endpointForDeployment(ModelDeployment{ProviderID: "native", Models: []string{"test"}, Capabilities: capabilities})
		if err != nil {
			t.Fatal(err)
		}
		client := endpoint.Provider.(StreamingClient)
		_, err = client.StreamChatCompletions(context.Background(), openai.ChatCompletionRequest{Model: "test"}, func(string) error { return nil })
		if stream {
			if err != nil || !streamRequested.Load() {
				t.Fatalf("native stream not enabled: %v", err)
			}
		} else if !errors.Is(err, ErrStreamingUnsupported) {
			t.Fatalf("stream unexpectedly enabled: %v", err)
		}
	}
}
