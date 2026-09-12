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

func TestManagedBedrockUsesConverseAdapter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/model/upstream:0/converse" || r.Header.Get("Authorization") != "Bearer bedrock-key" {
			t.Fatalf("path=%q authorization=%q", r.URL.EscapedPath(), r.Header.Get("Authorization"))
		}
		_, _ = fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`)
	}))
	defer server.Close()
	router := New(Config{CredentialEncryptionKey: []byte("managed-bedrock-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "bedrock", Type: "bedrock", BaseURL: server.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "bedrock-key", ProviderID: "bedrock", Secret: "bedrock-key"}); err != nil {
		t.Fatal(err)
	}
	endpoint, err := router.endpointForDeployment(ModelDeployment{ProviderID: "bedrock", CredentialID: "bedrock-key", UpstreamModel: "upstream:0", Models: []string{"public"}, Capabilities: []string{"chat", "tools"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	response, err := endpoint.Provider.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "upstream:0", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
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
		{providerType: "openai", capability: "interactions"},
		{providerType: "gemini", capability: "moderation"},
		{providerType: "gemini", capability: "fine_tuning"},
		{providerType: "mistral", capability: "image_generation"},
		{providerType: "mistral", capability: "search"},
		{providerType: "anthropic", capability: "embeddings"},
		{providerType: "bedrock", capability: "responses"},
		{providerType: "bedrock", capability: "structured_output"},
		{providerType: "groq", capability: "embeddings"},
		{providerType: "deepseek", capability: "embeddings"},
		{providerType: "deepseek", capability: "audio_transcription"},
		{providerType: "xai", capability: "rerank"},
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
		{providerType: "anthropic", capability: "background_responses"},
		{providerType: "anthropic", capability: "bedrock_invoke"},
		{providerType: "openai-compatible", capability: "video_extension"},
	}
	for _, test := range tests {
		t.Run(test.providerType+"/"+test.capability, func(t *testing.T) {
			router := New(Config{}).(*Router)
			if _, err := router.CreateProvider(ManagedProvider{ID: "provider", Type: test.providerType, BaseURL: "https://provider.example", Enabled: true}); err != nil {
				t.Fatal(err)
			}
			capabilities := []string{test.capability}
			switch test.capability {
			case "stream", "tools", "structured_output", "vision", "web_search", "web_fetch", "audio", "prompt_cache", "assistant_prefill", "bedrock_invoke":
				capabilities = append([]string{"chat"}, capabilities...)
			case "background_responses":
				capabilities = []string{"responses", "background_responses"}
			case "video_extension":
				capabilities = []string{"video", "video_extension"}
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
		{"background_responses"},
		{"background_interactions"},
		{"interaction_agents"},
		{"interaction_environment_reuse"},
		{"gemini_safety_settings"},
		{"interactions", "interaction_environment_reuse"},
		{"file_input"},
		{"bedrock_invoke"},
		{"video_extension"},
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
		{"chat", "gemini_safety_settings"},
		{"responses", "stream"},
		{"chat", "tools", "structured_output", "vision"},
		{"responses", "tools", "mcp"},
		{"responses", "background_responses"},
		{"interactions", "tools", "structured_output", "vision"},
		{"interactions", "interaction_agents"},
		{"interactions", "interaction_agents", "interaction_environment_reuse"},
		{"interactions", "background_interactions"},
		{"responses", "file_input"},
		{"chat", "web_search", "web_fetch", "audio", "prompt_cache", "assistant_prefill"},
		{"chat", "bedrock_invoke"},
		{"embeddings"},
		{"video", "video_extension"},
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
		{providerType: "gemini", capabilities: []string{"chat", "gemini_safety_settings", "image_generation", "image_edit", "image_variation", "audio_transcription", "audio_translation", "audio_speech", "ocr", "tools", "structured_output", "vision"}},
		{providerType: "cohere", capabilities: []string{"chat", "tools", "structured_output"}},
		{providerType: "bedrock", capabilities: []string{"chat", "tools", "prompt_cache", "bedrock_invoke"}},
		{providerType: "groq", capabilities: []string{"chat", "responses", "audio_transcription", "audio_translation", "audio_speech", "stream", "tools", "structured_output", "mcp", "vision"}},
		{providerType: "deepseek", capabilities: []string{"chat", "responses", "stream", "tools", "structured_output", "vision"}},
		{providerType: "openrouter", capabilities: []string{"chat", "responses", "embeddings", "rerank", "image_generation", "image_edit", "audio_transcription", "audio_speech", "stream", "tools", "structured_output", "vision", "web_search", "audio"}},
		{providerType: "mistral", capabilities: []string{"chat", "audio_transcription", "audio_speech", "tools", "structured_output", "vision", "assistant_prefill"}},
		{providerType: "xai", capabilities: []string{"chat", "video", "video_remix", "video_extension"}},
		{providerType: "openai-compatible", capabilities: []string{"chat", "responses", "background_responses", "audio_translation", "fine_tuning", "tools", "structured_output", "mcp", "vision", "web_search", "audio", "file_input"}},
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
	for _, operation := range []string{"chat", "responses", "embeddings", "rerank", "moderation", "image_generation", "image_edit", "image_variation", "audio_transcription", "audio_translation", "audio_speech", "search", "realtime", "stream"} {
		if !slices.Contains(byType["openai-compatible"], operation) {
			t.Fatalf("openai-compatible missing %s: %v", operation, byType["openai-compatible"])
		}
	}
	if !slices.Contains(byType["openai"], "fine_tuning") || !slices.Contains(byType["openai-compatible"], "fine_tuning") || slices.Contains(byType["gemini"], "fine_tuning") {
		t.Fatalf("fine-tuning profiles are incorrect: openai=%v compatible=%v gemini=%v", byType["openai"], byType["openai-compatible"], byType["gemini"])
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
	if slices.Contains(profilesByType["mistral"].Operations, "search") {
		t.Fatalf("mistral profile exposes unsupported search: %+v", profilesByType["mistral"])
	}
	if !slices.Contains(profilesByType["mistral"].Operations, "audio_transcription") || !slices.Contains(profilesByType["mistral"].Capabilities, "audio_transcription") {
		t.Fatalf("mistral profile is missing native transcription: %+v", profilesByType["mistral"])
	}
	if !slices.Equal(profilesByType["bedrock"].Operations, []string{"chat", "count_tokens", "stream", "bedrock_invoke"}) || !slices.Equal(profilesByType["bedrock"].Capabilities, []string{"chat", "stream", "bedrock_invoke", "tools", "vision", "prompt_cache"}) || !slices.Equal(profilesByType["bedrock"].AuthTypes, []string{"bearer", "aws_sigv4"}) {
		t.Fatalf("bedrock profile=%+v", profilesByType["bedrock"])
	}
	if !slices.Contains(profilesByType["anthropic"].Operations, "count_tokens") || !slices.Contains(profilesByType["gemini"].Operations, "count_tokens") || !slices.Equal(profilesByType["gemini"].AuthTypes, []string{"api_key", "gcp_adc"}) || !slices.Equal(profilesByType["azure-openai"].AuthTypes, []string{"api_key", "entra"}) {
		t.Fatalf("native count/auth profiles are incomplete: anthropic=%+v gemini=%+v azure=%+v", profilesByType["anthropic"], profilesByType["gemini"], profilesByType["azure-openai"])
	}
	if !slices.Contains(profilesByType["gemini"].Operations, "image_generation") || !slices.Contains(profilesByType["gemini"].Capabilities, "image_generation") || !slices.Contains(profilesByType["gemini"].Operations, "image_edit") || !slices.Contains(profilesByType["gemini"].Capabilities, "image_edit") || !slices.Contains(profilesByType["gemini"].Operations, "image_variation") || !slices.Contains(profilesByType["gemini"].Capabilities, "image_variation") {
		t.Fatalf("Gemini profile is missing native image operations: %+v", profilesByType["gemini"])
	}
	if !slices.Contains(profilesByType["gemini"].Capabilities, "gemini_safety_settings") {
		t.Fatalf("Gemini profile is missing native safety settings: %+v", profilesByType["gemini"])
	}
	if !slices.Contains(profilesByType["gemini"].Operations, "interactions") || slices.Contains(profilesByType["openai"].Operations, "interactions") {
		t.Fatalf("native interaction profiles are incorrect: gemini=%+v openai=%+v", profilesByType["gemini"], profilesByType["openai"])
	}
	for _, providerType := range []string{"anthropic", "gemini", "bedrock"} {
		if slices.Contains(profilesByType[providerType].Capabilities, "count_tokens") {
			t.Fatalf("adapter operation leaked into %s deployment capabilities: %+v", providerType, profilesByType[providerType])
		}
	}
	if slices.Contains(profilesByType["openai-compatible"].Operations, "count_tokens") {
		t.Fatalf("compatible adapter falsely advertises native token counting: %+v", profilesByType["openai-compatible"])
	}
	if !slices.Equal(profilesByType["groq"].Operations, []string{"chat", "responses", "audio_transcription", "audio_translation", "audio_speech", "stream"}) || !slices.Equal(profilesByType["groq"].Capabilities, []string{"chat", "responses", "audio_transcription", "audio_translation", "audio_speech", "stream", "tools", "structured_output", "mcp", "vision"}) {
		t.Fatalf("groq profile=%+v", profilesByType["groq"])
	}
	if !slices.Equal(profilesByType["deepseek"].Operations, []string{"chat", "responses", "stream"}) || !slices.Equal(profilesByType["deepseek"].Capabilities, []string{"chat", "responses", "stream", "tools", "structured_output", "vision"}) {
		t.Fatalf("deepseek profile=%+v", profilesByType["deepseek"])
	}
	if !slices.Equal(profilesByType["xai"].Operations, []string{"chat", "responses", "embeddings", "image_generation", "image_edit", "audio_transcription", "audio_speech", "video", "video_remix", "video_extension", "stream"}) || !slices.Equal(profilesByType["xai"].Capabilities, []string{"chat", "responses", "embeddings", "image_generation", "image_edit", "audio_transcription", "audio_speech", "video", "video_remix", "video_extension", "stream", "tools", "structured_output", "vision", "web_search"}) {
		t.Fatalf("xai profile=%+v", profilesByType["xai"])
	}
	if !slices.Equal(profilesByType["openrouter"].Operations, []string{"chat", "responses", "embeddings", "rerank", "image_generation", "image_edit", "audio_transcription", "audio_speech", "stream"}) || !slices.Equal(profilesByType["openrouter"].Capabilities, []string{"chat", "responses", "embeddings", "rerank", "image_generation", "image_edit", "audio_transcription", "audio_speech", "stream", "tools", "structured_output", "vision", "web_search", "audio"}) {
		t.Fatalf("openrouter profile=%+v", profilesByType["openrouter"])
	}
}

func TestManagedProviderCapabilityProfilesExposeValidatedChatParameters(t *testing.T) {
	profiles := ManagedProviderCapabilityProfiles()
	byType := make(map[string]ProviderChatParameterPolicy, len(profiles))
	for _, profile := range profiles {
		byType[profile.Type] = profile.ChatParameters
	}
	allReasoning := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	allTiers := []string{"auto", "default", "on_demand", "flex", "performance", "scale", "priority", "fast", "ultrafast", "standard_only"}
	tests := map[string]ProviderChatParameterPolicy{
		"demo":              {ReasoningEffort: []string{}, Logprobs: []string{}, ServiceTier: []string{}},
		"voyage":            {ReasoningEffort: []string{}, Logprobs: []string{}, ServiceTier: []string{}},
		"bedrock":           {ReasoningEffort: []string{}, Logprobs: []string{}, ServiceTier: []string{"default", "flex", "priority"}},
		"anthropic":         {ReasoningEffort: []string{"low", "medium", "high", "xhigh", "max"}, Logprobs: []string{}, ServiceTier: []string{"auto", "standard_only"}},
		"gemini":            {ReasoningEffort: []string{"minimal", "low", "medium", "high"}, Logprobs: []string{"false", "true"}, ServiceTier: []string{"auto", "default", "flex", "priority", "standard_only"}},
		"ollama":            {ReasoningEffort: []string{"none", "low", "medium", "high", "max"}, Logprobs: []string{"false", "true"}, ServiceTier: []string{}},
		"cohere":            {ReasoningEffort: []string{}, Logprobs: []string{"false", "true"}, ServiceTier: []string{}},
		"mistral":           {ReasoningEffort: []string{"none", "minimal", "low", "medium", "high", "xhigh"}, Logprobs: []string{}, ServiceTier: []string{}},
		"deepseek":          {ReasoningEffort: []string{}, Logprobs: []string{"false", "true"}, ServiceTier: []string{}},
		"xai":               {ReasoningEffort: []string{"none", "low", "medium", "high", "xhigh"}, Logprobs: []string{"false", "true"}, ServiceTier: []string{"default", "priority"}},
		"groq":              {ReasoningEffort: allReasoning, Logprobs: []string{}, ServiceTier: []string{"auto", "default", "on_demand", "flex", "performance"}},
		"openrouter":        {ReasoningEffort: allReasoning, Logprobs: []string{"false", "true"}, ServiceTier: allTiers},
		"openai-compatible": {ReasoningEffort: allReasoning, Logprobs: []string{"false", "true"}, ServiceTier: []string{}},
		"openai":            {ReasoningEffort: allReasoning, Logprobs: []string{"false", "true"}, ServiceTier: []string{"auto", "default", "flex", "priority"}},
		"azure-openai":      {ReasoningEffort: allReasoning, Logprobs: []string{"false", "true"}, ServiceTier: []string{}},
	}
	for providerType, expected := range tests {
		actual, found := byType[providerType]
		if !found || !slices.Equal(actual.ReasoningEffort, expected.ReasoningEffort) || !slices.Equal(actual.Logprobs, expected.Logprobs) || !slices.Equal(actual.ServiceTier, expected.ServiceTier) {
			t.Fatalf("%s chat parameters=%+v want=%+v", providerType, actual, expected)
		}
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
