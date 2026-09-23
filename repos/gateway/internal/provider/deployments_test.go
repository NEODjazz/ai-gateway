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
		{providerType: "anthropic", capability: "audio_input"},
		{providerType: "anthropic", capability: "video_input"},
		{providerType: "demo", capability: "tools"},
		{providerType: "voyage", capability: "structured_output"},
		{providerType: "cohere", capability: "vision"},
		{providerType: "anthropic", capability: "mcp"},
		{providerType: "anthropic", capability: "custom_tools"},
		{providerType: "anthropic", capability: "response_image_generation"},
		{providerType: "anthropic", capability: "response_computer"},
		{providerType: "anthropic", capability: "response_shell"},
		{providerType: "anthropic", capability: "response_apply_patch"},
		{providerType: "mistral", capability: "web_search"},
		{providerType: "openai", capability: "tool_search"},
		{providerType: "openai", capability: "computer_toolset"},
		{providerType: "openai", capability: "browser_toolset"},
		{providerType: "openai", capability: "thinking"},
		{providerType: "openai", capability: "zero_output"},
		{providerType: "openai", capability: "inference_geo"},
		{providerType: "openai", capability: "context_management"},
		{providerType: "openai", capability: "tool_result_error"},
		{providerType: "openai", capability: "document_citations"},
		{providerType: "openai", capability: "document_metadata"},
		{providerType: "openai", capability: "document_text"},
		{providerType: "ollama", capability: "prompt_cache"},
		{providerType: "openai", capability: "prompt_cache"},
		{providerType: "openai", capability: "assistant_prefill"},
		{providerType: "anthropic", capability: "background_responses"},
		{providerType: "anthropic", capability: "bedrock_invoke"},
		{providerType: "openai-compatible", capability: "video_extension"},
		{providerType: "openai-compatible", capability: "gemini_code_execution"},
		{providerType: "gemini", capability: "gemini_audio_timestamp"},
		{providerType: "openai-compatible", capability: "gemini_audio_timestamp"},
		{providerType: "openai-compatible", capability: "gemini_media_resolution"},
		{providerType: "openai-compatible", capability: "gemini_media_processing"},
		{providerType: "openai-compatible", capability: "gemini_search_time_range"},
		{providerType: "openai-compatible", capability: "gemini_file_search"},
		{providerType: "openai-compatible", capability: "gemini_computer_use"},
		{providerType: "openai-compatible", capability: "gemini_mcp"},
		{providerType: "openai-compatible", capability: "url_context"},
		{providerType: "openai-compatible", capability: "google_maps"},
		{providerType: "vertex-gemini", capability: "responses"},
		{providerType: "vertex-gemini", capability: "interactions"},
		{providerType: "vertex-gemini", capability: "image_generation"},
		{providerType: "vertex-gemini", capability: "cached_content"},
		{providerType: "vertex-gemini", capability: "google_maps"},
	}
	for _, test := range tests {
		t.Run(test.providerType+"/"+test.capability, func(t *testing.T) {
			router := New(Config{}).(*Router)
			baseURL := "https://provider.example"
			if test.providerType == "vertex-gemini" {
				baseURL = "https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1/publishers/google"
			}
			if _, err := router.CreateProvider(ManagedProvider{ID: "provider", Type: test.providerType, BaseURL: baseURL, Enabled: true}); err != nil {
				t.Fatal(err)
			}
			capabilities := []string{test.capability}
			switch test.capability {
			case "stream", "tools", "structured_output", "vision", "web_search", "web_fetch", "tool_search", "memory_tool", "bash_tool", "text_editor_tool", "computer_toolset", "browser_toolset", "thinking", "zero_output", "inference_geo", "context_management", "tool_result_error", "document_citations", "document_metadata", "document_text", "audio", "audio_input", "video_input", "prompt_cache", "assistant_prefill", "bedrock_invoke", "gemini_code_execution", "url_context", "google_maps", "cached_content":
				capabilities = append([]string{"chat"}, capabilities...)
			case "gemini_audio_timestamp":
				capabilities = []string{"chat", "audio_input", "gemini_audio_timestamp"}
			case "gemini_media_resolution":
				capabilities = []string{"chat", "vision", "gemini_media_resolution"}
			case "gemini_media_processing":
				capabilities = []string{"chat", "video_input", "gemini_media_processing"}
			case "gemini_search_time_range":
				capabilities = []string{"chat", "web_search", "gemini_search_time_range"}
			case "gemini_file_search":
				capabilities = []string{"chat", "gemini_file_search"}
			case "gemini_computer_use":
				capabilities = []string{"chat", "gemini_computer_use"}
			case "gemini_mcp":
				capabilities = []string{"chat", "gemini_mcp"}
			case "background_responses":
				capabilities = []string{"responses", "background_responses"}
			case "video_extension":
				capabilities = []string{"video", "video_extension"}
			case "mcp":
				capabilities = []string{"responses", "tools", "mcp"}
			case "custom_tools":
				capabilities = []string{"responses", "tools", "custom_tools"}
			case "response_image_generation":
				capabilities = []string{"responses", "tools", "response_image_generation"}
			case "response_computer":
				capabilities = []string{"responses", "tools", "response_computer"}
			case "response_shell":
				capabilities = []string{"responses", "tools", "response_shell"}
			case "response_apply_patch":
				capabilities = []string{"responses", "tools", "response_apply_patch"}
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
		{"custom_tools"},
		{"responses", "custom_tools"},
		{"tools", "custom_tools"},
		{"response_image_generation"},
		{"responses", "response_image_generation"},
		{"tools", "response_image_generation"},
		{"response_computer"},
		{"responses", "response_computer"},
		{"tools", "response_computer"},
		{"response_shell"},
		{"responses", "response_shell"},
		{"tools", "response_shell"},
		{"response_apply_patch"},
		{"responses", "response_apply_patch"},
		{"tools", "response_apply_patch"},
		{"responses", "mcp"},
		{"chat", "mcp", "tools"},
		{"web_search"},
		{"tool_search"},
		{"memory_tool"},
		{"bash_tool"},
		{"text_editor_tool"},
		{"computer_toolset"},
		{"browser_toolset"},
		{"audio_input"},
		{"video_input"},
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
		{"document_text"},
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
		{"chat", "audio_input"},
		{"chat", "video_input"},
		{"chat", "gemini_safety_settings"},
		{"responses", "stream"},
		{"chat", "tools", "structured_output", "vision"},
		{"responses", "tools", "mcp"},
		{"responses", "tools", "custom_tools"},
		{"responses", "tools", "response_image_generation"},
		{"responses", "tools", "response_computer"},
		{"responses", "tools", "response_shell"},
		{"responses", "tools", "response_apply_patch"},
		{"responses", "background_responses"},
		{"interactions", "tools", "structured_output", "vision"},
		{"interactions", "interaction_agents"},
		{"interactions", "interaction_agents", "interaction_environment_reuse"},
		{"interactions", "background_interactions"},
		{"responses", "file_input"},
		{"chat", "web_search", "web_fetch", "tool_search", "memory_tool", "bash_tool", "text_editor_tool", "computer_toolset", "browser_toolset", "thinking", "zero_output", "inference_geo", "context_management", "tool_result_error", "document_citations", "document_metadata", "document_text", "audio", "prompt_cache", "assistant_prefill"},
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

func TestManagedAnthropicRejectsResponsesWebSearchCombination(t *testing.T) {
	router := New(Config{}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "provider", Type: "anthropic", BaseURL: "https://provider.example", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	_, err := router.CreateModelDeployment(ModelDeployment{ID: "deployment", ProviderID: "provider", Models: []string{"model"}, Capabilities: []string{"chat", "responses", "tools", "web_search"}, Enabled: true})
	if !errors.Is(err, ErrUnsupportedProviderCapability) {
		t.Fatalf("unsupported Responses web search combination accepted: %v", err)
	}
}

func TestManagedDeploymentAcceptsSupportedFeatureCapabilities(t *testing.T) {
	tests := []struct {
		providerType string
		capabilities []string
	}{
		{providerType: "ollama", capabilities: []string{"chat", "tools", "structured_output", "vision"}},
		{providerType: "anthropic", capabilities: []string{"chat", "tools", "structured_output", "vision", "web_search", "web_fetch", "tool_search", "prompt_cache", "assistant_prefill", "memory_tool", "bash_tool", "text_editor_tool", "computer_toolset", "browser_toolset", "thinking", "zero_output", "inference_geo", "context_management", "tool_result_error", "document_citations", "document_metadata", "document_text", "file_input"}},
		{providerType: "gemini", capabilities: []string{"chat", "gemini_safety_settings", "gemini_code_execution", "gemini_media_resolution", "gemini_media_processing", "gemini_search_time_range", "gemini_file_search", "gemini_computer_use", "gemini_mcp", "url_context", "google_maps", "image_generation", "image_edit", "image_variation", "audio_transcription", "audio_translation", "audio_speech", "ocr", "tools", "structured_output", "vision", "web_search", "audio_input", "video_input", "file_input"}},
		{providerType: "vertex-gemini", capabilities: []string{"chat", "embeddings", "stream", "gemini_safety_settings", "gemini_code_execution", "gemini_audio_timestamp", "gemini_media_resolution", "gemini_media_processing", "gemini_search_time_range", "gemini_file_search", "gemini_computer_use", "gemini_mcp", "url_context", "tools", "structured_output", "vision", "web_search", "audio_input", "video_input", "file_input"}},
		{providerType: "cohere", capabilities: []string{"chat", "tools", "structured_output"}},
		{providerType: "bedrock", capabilities: []string{"chat", "tools", "prompt_cache", "bedrock_invoke"}},
		{providerType: "groq", capabilities: []string{"chat", "responses", "audio_transcription", "audio_translation", "audio_speech", "stream", "tools", "structured_output", "mcp", "vision"}},
		{providerType: "deepseek", capabilities: []string{"chat", "responses", "stream", "tools", "structured_output", "vision"}},
		{providerType: "openrouter", capabilities: []string{"chat", "responses", "embeddings", "rerank", "image_generation", "image_edit", "audio_transcription", "audio_speech", "stream", "tools", "custom_tools", "structured_output", "vision", "web_search", "audio"}},
		{providerType: "mistral", capabilities: []string{"chat", "audio_transcription", "audio_speech", "tools", "structured_output", "vision", "assistant_prefill"}},
		{providerType: "xai", capabilities: []string{"chat", "responses", "tools", "custom_tools", "video", "video_remix", "video_extension"}},
		{providerType: "openai-compatible", capabilities: []string{"chat", "responses", "background_responses", "audio_translation", "fine_tuning", "container", "container_files", "container_network", "tools", "custom_tools", "response_computer", "response_shell", "response_apply_patch", "structured_output", "mcp", "vision", "web_search", "audio", "file_input"}},
	}
	for _, test := range tests {
		t.Run(test.providerType, func(t *testing.T) {
			router := New(Config{}).(*Router)
			baseURL := "https://provider.example"
			if test.providerType == "vertex-gemini" {
				baseURL = "https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1/publishers/google"
			}
			if _, err := router.CreateProvider(ManagedProvider{ID: "provider", Type: test.providerType, BaseURL: baseURL, Enabled: true}); err != nil {
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
	for _, operation := range []string{"chat", "completions", "responses", "embeddings", "rerank", "moderation", "image_generation", "image_edit", "image_variation", "audio_transcription", "audio_translation", "audio_speech", "search", "container", "container_files", "container_network", "realtime", "stream"} {
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
	if !slices.Contains(profilesByType["anthropic"].Capabilities, "thinking") || slices.Contains(profilesByType["openai"].Capabilities, "thinking") {
		t.Fatalf("thinking profiles are incorrect: anthropic=%v openai=%v", profilesByType["anthropic"].Capabilities, profilesByType["openai"].Capabilities)
	}
	if !slices.Contains(profilesByType["anthropic"].Capabilities, "zero_output") || slices.Contains(profilesByType["openai"].Capabilities, "zero_output") {
		t.Fatalf("zero output profiles are incorrect: anthropic=%v openai=%v", profilesByType["anthropic"].Capabilities, profilesByType["openai"].Capabilities)
	}
	if !slices.Contains(profilesByType["anthropic"].Capabilities, "inference_geo") || slices.Contains(profilesByType["openai"].Capabilities, "inference_geo") {
		t.Fatalf("inference geography profiles are incorrect: anthropic=%v openai=%v", profilesByType["anthropic"].Capabilities, profilesByType["openai"].Capabilities)
	}
	if !slices.Contains(profilesByType["anthropic"].Capabilities, "context_management") || slices.Contains(profilesByType["openai"].Capabilities, "context_management") {
		t.Fatalf("context management profiles are incorrect: anthropic=%v openai=%v", profilesByType["anthropic"].Capabilities, profilesByType["openai"].Capabilities)
	}
	if !slices.Contains(profilesByType["anthropic"].Capabilities, "tool_result_error") || slices.Contains(profilesByType["openai"].Capabilities, "tool_result_error") {
		t.Fatalf("tool-result error profiles are incorrect: anthropic=%v openai=%v", profilesByType["anthropic"].Capabilities, profilesByType["openai"].Capabilities)
	}
	if !slices.Contains(profilesByType["anthropic"].Capabilities, "document_citations") || slices.Contains(profilesByType["openai"].Capabilities, "document_citations") {
		t.Fatalf("document citations profiles are incorrect: anthropic=%v openai=%v", profilesByType["anthropic"].Capabilities, profilesByType["openai"].Capabilities)
	}
	if !slices.Contains(profilesByType["anthropic"].Capabilities, "document_metadata") || slices.Contains(profilesByType["openai"].Capabilities, "document_metadata") {
		t.Fatalf("document metadata profiles are incorrect: anthropic=%v openai=%v", profilesByType["anthropic"].Capabilities, profilesByType["openai"].Capabilities)
	}
	if !slices.Contains(profilesByType["anthropic"].Capabilities, "document_text") || slices.Contains(profilesByType["openai"].Capabilities, "document_text") {
		t.Fatalf("text document profiles are incorrect: anthropic=%v openai=%v", profilesByType["anthropic"].Capabilities, profilesByType["openai"].Capabilities)
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
	if !slices.Contains(profilesByType["gemini"].Capabilities, "web_search") {
		t.Fatalf("Gemini profile is missing native Google Search: %+v", profilesByType["gemini"])
	}
	if !slices.Contains(profilesByType["gemini"].Capabilities, "gemini_code_execution") {
		t.Fatalf("Gemini profile is missing native code execution: %+v", profilesByType["gemini"])
	}
	if slices.Contains(profilesByType["gemini"].Capabilities, "gemini_audio_timestamp") || !slices.Contains(profilesByType["vertex-gemini"].Capabilities, "gemini_audio_timestamp") {
		t.Fatalf("Vertex audio timestamp profiles are incorrect: gemini=%+v vertex=%+v", profilesByType["gemini"], profilesByType["vertex-gemini"])
	}
	if !slices.Contains(profilesByType["gemini"].Capabilities, "gemini_media_resolution") || !slices.Contains(profilesByType["vertex-gemini"].Capabilities, "gemini_media_resolution") {
		t.Fatalf("Gemini media resolution profiles are incomplete: gemini=%+v vertex=%+v", profilesByType["gemini"], profilesByType["vertex-gemini"])
	}
	if !slices.Contains(profilesByType["gemini"].Capabilities, "gemini_media_processing") || !slices.Contains(profilesByType["vertex-gemini"].Capabilities, "gemini_media_processing") {
		t.Fatalf("Gemini media processing profiles are incomplete: gemini=%+v vertex=%+v", profilesByType["gemini"], profilesByType["vertex-gemini"])
	}
	if !slices.Contains(profilesByType["gemini"].Capabilities, "gemini_search_time_range") || !slices.Contains(profilesByType["vertex-gemini"].Capabilities, "gemini_search_time_range") {
		t.Fatalf("Gemini search time-range profiles are incomplete: gemini=%+v vertex=%+v", profilesByType["gemini"], profilesByType["vertex-gemini"])
	}
	if !slices.Contains(profilesByType["gemini"].Capabilities, "gemini_file_search") || !slices.Contains(profilesByType["vertex-gemini"].Capabilities, "gemini_file_search") {
		t.Fatalf("Gemini file search profiles are incomplete: gemini=%+v vertex=%+v", profilesByType["gemini"], profilesByType["vertex-gemini"])
	}
	if !slices.Contains(profilesByType["gemini"].Capabilities, "gemini_computer_use") || !slices.Contains(profilesByType["vertex-gemini"].Capabilities, "gemini_computer_use") {
		t.Fatalf("Gemini computer use profiles are incomplete: gemini=%+v vertex=%+v", profilesByType["gemini"], profilesByType["vertex-gemini"])
	}
	if !slices.Contains(profilesByType["gemini"].Capabilities, "url_context") {
		t.Fatalf("Gemini profile is missing native URL context: %+v", profilesByType["gemini"])
	}
	if !slices.Contains(profilesByType["gemini"].Capabilities, "google_maps") {
		t.Fatalf("Gemini profile is missing Google Maps grounding: %+v", profilesByType["gemini"])
	}
	if !slices.Contains(profilesByType["gemini"].Capabilities, "audio_input") {
		t.Fatalf("Gemini profile is missing native inline audio: %+v", profilesByType["gemini"])
	}
	if !slices.Contains(profilesByType["gemini"].Capabilities, "file_input") {
		t.Fatalf("Gemini profile is missing native inline files: %+v", profilesByType["gemini"])
	}
	if !slices.Contains(profilesByType["anthropic"].Capabilities, "file_input") {
		t.Fatalf("Anthropic profile is missing native inline PDF documents: %+v", profilesByType["anthropic"])
	}
	if !slices.Contains(profilesByType["gemini"].Capabilities, "video_input") {
		t.Fatalf("Gemini profile is missing native inline video: %+v", profilesByType["gemini"])
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
	if !slices.Equal(profilesByType["xai"].Operations, []string{"chat", "responses", "embeddings", "image_generation", "image_edit", "audio_transcription", "audio_speech", "video", "video_remix", "video_extension", "stream"}) || !slices.Equal(profilesByType["xai"].Capabilities, []string{"chat", "responses", "embeddings", "image_generation", "image_edit", "audio_transcription", "audio_speech", "video", "video_remix", "video_extension", "stream", "tools", "custom_tools", "structured_output", "vision", "web_search"}) {
		t.Fatalf("xai profile=%+v", profilesByType["xai"])
	}
	if !slices.Equal(profilesByType["openrouter"].Operations, []string{"chat", "completions", "responses", "embeddings", "rerank", "image_generation", "image_edit", "audio_transcription", "audio_speech", "stream"}) || !slices.Equal(profilesByType["openrouter"].Capabilities, []string{"chat", "completions", "responses", "embeddings", "rerank", "image_generation", "image_edit", "audio_transcription", "audio_speech", "stream", "tools", "custom_tools", "structured_output", "vision", "web_search", "audio"}) {
		t.Fatalf("openrouter profile=%+v", profilesByType["openrouter"])
	}
}

func TestManagedProviderCapabilityProfilesExposeValidatedChatParameters(t *testing.T) {
	profiles := ManagedProviderCapabilityProfiles()
	byType := make(map[string]ProviderChatParameterPolicy, len(profiles))
	for _, profile := range profiles {
		byType[profile.Type] = profile.ChatParameters
	}
	allReasoning := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "default"}
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
		"groq":              {ReasoningEffort: []string{}, ReasoningFormat: []string{}, Logprobs: []string{}, ServiceTier: []string{"auto", "on_demand", "flex", "performance"}},
		"openrouter":        {ReasoningEffort: allReasoning, Logprobs: []string{"false", "true"}, ServiceTier: allTiers},
		"openai-compatible": {ReasoningEffort: allReasoning, Logprobs: []string{"false", "true"}, ServiceTier: []string{}},
		"openai":            {ReasoningEffort: allReasoning, Logprobs: []string{"false", "true"}, ServiceTier: []string{"auto", "default", "flex", "priority", "fast", "ultrafast"}},
		"azure-openai":      {ReasoningEffort: allReasoning, Logprobs: []string{"false", "true"}, ServiceTier: []string{}},
	}
	for providerType, expected := range tests {
		actual, found := byType[providerType]
		if !found || !slices.Equal(actual.ReasoningEffort, expected.ReasoningEffort) || !slices.Equal(actual.ReasoningFormat, expected.ReasoningFormat) || !slices.Equal(actual.Logprobs, expected.Logprobs) || !slices.Equal(actual.ServiceTier, expected.ServiceTier) {
			t.Fatalf("%s chat parameters=%+v want=%+v", providerType, actual, expected)
		}
	}
}

func TestManagedProviderCapabilityProfilesExposeAllValidatedChatOptions(t *testing.T) {
	profiles := ManagedProviderCapabilityProfiles()
	byType := make(map[string][]string, len(profiles))
	for _, profile := range profiles {
		byType[profile.Type] = profile.ChatParameters.SupportedOptions
	}
	compatible := []string{"metadata", "modalities", "audio", "moderation", "n", "safety_identifier", "prompt_cache_key", "prompt_cache_options", "prompt_cache_retention", "prediction", "user", "verbosity", "web_search_options", "web_fetch_options", "logprobs", "top_logprobs", "frequency_penalty", "presence_penalty", "min_p", "top_k", "top_a", "repetition_penalty", "logit_bias", "reasoning_effort"}
	openRouter := []string{"metadata", "modalities", "audio", "n", "safety_identifier", "prompt_cache_key", "prompt_cache_options", "prompt_cache_retention", "prediction", "user", "verbosity", "web_search_options", "web_fetch_options", "logprobs", "top_logprobs", "frequency_penalty", "presence_penalty", "min_p", "top_k", "top_a", "repetition_penalty", "logit_bias", "reasoning_effort", "service_tier"}
	compatibleTiered := append(append([]string(nil), compatible...), "service_tier")
	expected := map[string][]string{
		"demo": {}, "voyage": {}, "opensandbox": {},
		"ollama":            {"logprobs", "top_logprobs", "min_p", "top_k", "reasoning_effort"},
		"openai":            compatibleTiered,
		"openai-compatible": compatible,
		"openrouter":        openRouter,
		"azure-openai":      compatible,
		"anthropic":         {"metadata", "web_search_options", "web_fetch_options", "top_k", "reasoning_effort", "service_tier"},
		"gemini":            {"store", "modalities", "n", "web_search_options", "logprobs", "top_logprobs", "frequency_penalty", "presence_penalty", "top_k", "reasoning_effort", "service_tier"},
		"cohere":            {"logprobs", "frequency_penalty", "presence_penalty", "top_k"},
		"mistral":           {"metadata", "safe_prompt", "n", "prompt_cache_key", "prompt_mode", "prediction", "frequency_penalty", "presence_penalty", "reasoning_effort"},
		"bedrock":           {"service_tier"},
		"groq":              {"user", "service_tier"},
		"deepseek":          {"user", "logprobs", "top_logprobs"},
		"xai":               {"n", "prompt_cache_key", "user", "web_search_options", "logprobs", "top_logprobs", "frequency_penalty", "presence_penalty", "reasoning_effort", "service_tier"},
	}
	for providerType, want := range expected {
		if got, found := byType[providerType]; !found || !slices.Equal(got, want) {
			t.Errorf("%s supported options=%v want=%v", providerType, got, want)
		}
	}
}

func TestClearThinkingIsOnlyAdvertisedForCerebrasModel(t *testing.T) {
	for _, profile := range ManagedProviderCapabilityProfiles() {
		if slices.Contains(profile.ChatParameters.SupportedOptions, "clear_thinking") {
			t.Fatalf("%s advertises provider-wide clear_thinking", profile.Type)
		}
		for _, policy := range profile.ChatModelParameters {
			advertised := slices.Contains(policy.SupportedOptions, "clear_thinking")
			if advertised != (profile.Type == "cerebras" && policy.Model == "zai-glm-4.7") {
				t.Fatalf("%s/%s clear_thinking=%v", profile.Type, policy.Model, advertised)
			}
		}
	}
}

func TestManagedProviderCapabilityProfilesExposeAllValidatedResponseOptions(t *testing.T) {
	profiles := ManagedProviderCapabilityProfiles()
	byType := make(map[string]ProviderResponseParameterPolicy, len(profiles))
	for _, profile := range profiles {
		byType[profile.Type] = profile.ResponseParameters
	}
	allReasoning := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "default"}
	allTiers := []string{"auto", "default", "on_demand", "flex", "performance", "scale", "priority", "fast", "ultrafast", "standard_only"}
	compatible := []string{"metadata", "context_management", "moderation", "top_logprobs", "truncation", "store", "include", "parallel_tool_calls", "text.verbosity", "previous_response_id", "user", "safety_identifier", "prompt_cache_key", "prompt_cache_options", "prompt_cache_retention", "stream_options", "max_output_tokens", "max_tokens", "temperature", "top_p", "frequency_penalty", "presence_penalty", "max_tool_calls", "reasoning"}
	tiered := append(append([]string(nil), compatible...), "service_tier")
	expected := map[string]ProviderResponseParameterPolicy{
		"demo": {}, "gemini": {}, "cohere": {}, "mistral": {}, "voyage": {}, "bedrock": {}, "opensandbox": {},
		"ollama":            {SupportedOptions: []string{"metadata", "top_logprobs", "truncation", "store", "include", "parallel_tool_calls", "previous_response_id", "max_output_tokens", "max_tokens", "temperature", "top_p", "reasoning"}, ReasoningEffort: allReasoning},
		"openai":            {SupportedOptions: tiered, ReasoningEffort: allReasoning, ServiceTier: []string{"auto", "default", "flex", "priority", "fast", "ultrafast"}},
		"openai-compatible": {SupportedOptions: compatible, ReasoningEffort: allReasoning},
		"openrouter":        {SupportedOptions: tiered, ReasoningEffort: allReasoning, ServiceTier: allTiers},
		"azure-openai":      {SupportedOptions: compatible, ReasoningEffort: allReasoning},
		"anthropic":         {SupportedOptions: []string{"parallel_tool_calls", "max_output_tokens", "max_tokens", "temperature", "top_p"}},
		"groq":              {SupportedOptions: []string{"metadata", "parallel_tool_calls", "user", "max_output_tokens", "max_tokens", "temperature", "top_p", "reasoning", "service_tier"}, ReasoningEffort: []string{"low", "medium", "high"}, ServiceTier: []string{"auto", "default", "flex"}},
		"deepseek":          {SupportedOptions: []string{"top_logprobs", "user", "max_output_tokens", "max_tokens", "temperature", "top_p", "reasoning"}, ReasoningEffort: []string{"low", "medium", "high", "xhigh", "max"}},
		"xai":               {SupportedOptions: []string{"store", "include", "parallel_tool_calls", "previous_response_id", "user", "prompt_cache_key", "max_output_tokens", "max_tokens", "temperature", "top_p", "max_tool_calls", "reasoning", "service_tier"}, ReasoningEffort: []string{"none", "low", "medium", "high", "xhigh"}, ServiceTier: []string{"default", "priority"}},
	}
	for providerType, want := range expected {
		got, found := byType[providerType]
		if !found || !slices.Equal(got.SupportedOptions, want.SupportedOptions) || !slices.Equal(got.ReasoningEffort, want.ReasoningEffort) || !slices.Equal(got.ServiceTier, want.ServiceTier) {
			t.Errorf("%s response parameters=%+v want=%+v", providerType, got, want)
		}
	}
}

func TestManagedProviderCapabilityProfilesExposeValidatedInteractionOptions(t *testing.T) {
	wantOptions := []string{
		"agent", "environment", "system_instruction", "tools", "response_format", "response_mime_type", "previous_interaction_id", "store", "stream", "background",
		"generation_config.max_output_tokens", "generation_config.temperature", "generation_config.top_p", "generation_config.seed", "generation_config.stop_sequences", "generation_config.thinking_level",
	}
	for _, profile := range ManagedProviderCapabilityProfiles() {
		listed := profile.Type == "gemini"
		wantInputs, wantLevels := []string(nil), []string(nil)
		options := []string(nil)
		if listed {
			options = wantOptions
			wantInputs = []string{"string", "steps"}
			wantLevels = []string{"minimal", "low", "medium", "high"}
		}
		got := profile.InteractionParameters
		if slices.Contains(profile.Operations, "interactions") != listed || !slices.Equal(got.SupportedOptions, options) || !slices.Equal(got.InputForms, wantInputs) || !slices.Equal(got.ThinkingLevels, wantLevels) {
			t.Errorf("%s interaction parameters=%+v operation=%v", profile.Type, got, slices.Contains(profile.Operations, "interactions"))
		}
	}
}

func TestManagedProviderCapabilityProfilesExposeValidatedEmbeddingAndRerankOptions(t *testing.T) {
	profiles := ManagedProviderCapabilityProfiles()
	byType := make(map[string]ProviderCapabilityProfile, len(profiles))
	for _, profile := range profiles {
		byType[profile.Type] = profile
		if slices.Contains(profile.Operations, "embeddings") != (len(profile.EmbeddingParameters.InputForms) > 0) {
			t.Errorf("%s embedding profile does not match operations: %+v", profile.Type, profile.EmbeddingParameters)
		}
		if slices.Contains(profile.Operations, "rerank") != (len(profile.RerankParameters.DocumentForms) > 0) {
			t.Errorf("%s rerank profile does not match operations: %+v", profile.Type, profile.RerankParameters)
		}
	}
	assertEmbedding := func(providerType string, want ProviderEmbeddingParameterPolicy) {
		got := byType[providerType].EmbeddingParameters
		if !slices.Equal(got.SupportedOptions, want.SupportedOptions) || !slices.Equal(got.InputForms, want.InputForms) || !slices.Equal(got.InputTypes, want.InputTypes) || !slices.Equal(got.EncodingFormats, want.EncodingFormats) || !slices.Equal(got.OutputDTypes, want.OutputDTypes) {
			t.Errorf("%s embedding parameters=%+v want=%+v", providerType, got, want)
		}
	}
	assertEmbedding("demo", ProviderEmbeddingParameterPolicy{SupportedOptions: []string{"dimensions", "encoding_format"}, InputForms: []string{"text", "text_array"}, EncodingFormats: []string{"float"}})
	assertEmbedding("cohere", ProviderEmbeddingParameterPolicy{SupportedOptions: []string{"dimensions", "input_type", "encoding_format"}, InputForms: []string{"text", "text_array"}, InputTypes: []string{"search_query", "search_document", "classification", "clustering"}, EncodingFormats: []string{"float", "base64"}})
	assertEmbedding("vertex-gemini", ProviderEmbeddingParameterPolicy{SupportedOptions: []string{"dimensions", "input_type", "encoding_format"}, InputForms: []string{"text", "text_array"}, InputTypes: []string{"search_query", "search_document", "classification", "clustering"}, EncodingFormats: []string{"float"}})
	assertEmbedding("voyage", ProviderEmbeddingParameterPolicy{SupportedOptions: []string{"dimensions", "input_type", "encoding_format", "output_dtype"}, InputForms: []string{"text", "text_array"}, InputTypes: []string{"query", "document"}, EncodingFormats: []string{"float", "base64"}, OutputDTypes: []string{"float", "int8", "uint8", "binary", "ubinary"}})
	if got := byType["openrouter"].RerankParameters; !slices.Equal(got.SupportedOptions, []string{"top_n", "return_documents"}) || !slices.Equal(got.DocumentForms, []string{"text", "object"}) {
		t.Errorf("openrouter rerank parameters=%+v", got)
	}
	if got := byType["cohere"].RerankParameters; !slices.Equal(got.SupportedOptions, []string{"top_n", "return_documents", "max_tokens_per_doc"}) || !slices.Equal(got.DocumentForms, []string{"text"}) {
		t.Errorf("cohere rerank parameters=%+v", got)
	}
	if got := byType["nvidia-nim"].RerankParameters; !slices.Equal(got.SupportedOptions, []string{"top_n", "return_documents", "truncate"}) || !slices.Equal(got.DocumentForms, []string{"text"}) {
		t.Errorf("nvidia-nim rerank parameters=%+v", got)
	}
}

func TestManagedProviderCapabilityProfilesExposeValidatedCompletionOptions(t *testing.T) {
	profiles := ManagedProviderCapabilityProfiles()
	byType := make(map[string]ProviderCapabilityProfile, len(profiles))
	for _, profile := range profiles {
		byType[profile.Type] = profile
		if slices.Contains(profile.Operations, "completions") != (len(profile.CompletionParameters.PromptForms) > 0) {
			t.Errorf("%s completion profile does not match operations: %+v", profile.Type, profile.CompletionParameters)
		}
	}
	compatibleOptions := []string{"best_of", "echo", "frequency_penalty", "logit_bias", "logprobs", "max_tokens", "n", "presence_penalty", "seed", "stop", "suffix", "temperature", "top_p", "user"}
	for _, providerType := range []string{"openai", "openai-compatible", "openrouter", "azure-openai"} {
		got := byType[providerType].CompletionParameters
		if !slices.Equal(got.SupportedOptions, compatibleOptions) || !slices.Equal(got.PromptForms, []string{"text", "text_array", "token_array", "token_batch"}) {
			t.Errorf("%s completion parameters=%+v", providerType, got)
		}
	}
	if got := byType["ollama"].CompletionParameters; !slices.Equal(got.SupportedOptions, compatibleOptions) || !slices.Equal(got.PromptForms, []string{"text"}) {
		t.Errorf("ollama completion parameters=%+v", got)
	}
	if got := byType["mistral"].CompletionParameters; !slices.Equal(got.SupportedOptions, []string{"metadata", "max_tokens", "min_tokens", "prompt_cache_key", "seed", "stop", "suffix", "temperature", "top_p"}) || !slices.Equal(got.PromptForms, []string{"text"}) {
		t.Errorf("mistral completion parameters=%+v", got)
	}
}

func TestManagedProviderCapabilityProfilesExposeValidatedModerationOptions(t *testing.T) {
	profiles := ManagedProviderCapabilityProfiles()
	for _, profile := range profiles {
		hasOperation := slices.Contains(profile.Operations, "moderation")
		if hasOperation != (len(profile.ModerationParameters.InputForms) > 0) {
			t.Errorf("%s moderation profile does not match operations: %+v", profile.Type, profile.ModerationParameters)
		}
		if !hasOperation {
			continue
		}
		if profile.Type == "mistral" {
			if !slices.Equal(profile.ModerationParameters.SupportedOptions, []string{"metadata"}) || !slices.Equal(profile.ModerationParameters.InputForms, []string{"text", "text_array"}) {
				t.Errorf("mistral moderation parameters=%+v", profile.ModerationParameters)
			}
			continue
		}
		if len(profile.ModerationParameters.SupportedOptions) != 0 || !slices.Equal(profile.ModerationParameters.InputForms, []string{"text", "text_array", "content_parts"}) {
			t.Errorf("%s moderation parameters=%+v", profile.Type, profile.ModerationParameters)
		}
	}
}

func TestManagedProviderCapabilityProfilesExposeValidatedSearchOptions(t *testing.T) {
	wantOptions := []string{"max_results", "search_domain_filter", "max_tokens_per_page", "country"}
	wantQueries := []string{"text", "text_array"}
	for _, profile := range ManagedProviderCapabilityProfiles() {
		hasOperation := slices.Contains(profile.Operations, "search")
		if hasOperation != (len(profile.SearchParameters.QueryForms) > 0) {
			t.Errorf("%s search profile does not match operations: %+v", profile.Type, profile.SearchParameters)
		}
		if hasOperation && (!slices.Equal(profile.SearchParameters.SupportedOptions, wantOptions) || !slices.Equal(profile.SearchParameters.QueryForms, wantQueries)) {
			t.Errorf("%s search parameters=%+v", profile.Type, profile.SearchParameters)
		}
	}
}

func TestManagedProviderCapabilityProfilesExposeValidatedImageGenerationOptions(t *testing.T) {
	all := []string{"n", "quality", "response_format", "size", "style", "user", "background", "output_format", "output_compression", "resolution", "aspect_ratio", "seed"}
	expected := map[string][]string{
		"openai": all, "openai-compatible": all, "azure-openai": all,
		"openrouter": {"n", "quality", "size", "user", "background", "output_format", "output_compression", "resolution", "aspect_ratio", "seed"},
		"gemini":     {"n", "response_format", "resolution", "aspect_ratio"},
		"together":   {"n", "response_format", "size", "output_format", "seed"},
		"xai":        {"n", "quality", "response_format", "resolution", "aspect_ratio"},
	}
	for _, profile := range ManagedProviderCapabilityProfiles() {
		hasOperation := slices.Contains(profile.Operations, "image_generation")
		want, listed := expected[profile.Type]
		if hasOperation != listed || !slices.Equal(profile.ImageGenerationParameters.SupportedOptions, want) {
			t.Errorf("%s image generation parameters=%v operation=%v", profile.Type, profile.ImageGenerationParameters.SupportedOptions, hasOperation)
		}
	}
}

func TestManagedProviderCapabilityProfilesExposeValidatedImageEditOptions(t *testing.T) {
	all := []string{"mask", "n", "quality", "response_format", "size", "user", "background", "output_format", "output_compression"}
	expected := map[string]ProviderImageEditParameterPolicy{
		"openai": {SupportedOptions: all, MaxImages: 8}, "openai-compatible": {SupportedOptions: all, MaxImages: 8}, "azure-openai": {SupportedOptions: all, MaxImages: 8},
		"openrouter": {SupportedOptions: []string{"n", "quality", "size", "user", "background", "output_format", "output_compression"}, MaxImages: 8},
		"gemini":     {SupportedOptions: []string{"n", "response_format"}, MaxImages: 8},
		"xai":        {SupportedOptions: []string{"n", "quality", "response_format"}, MaxImages: 5},
	}
	for _, profile := range ManagedProviderCapabilityProfiles() {
		want, listed := expected[profile.Type]
		if slices.Contains(profile.Operations, "image_edit") != listed || !slices.Equal(profile.ImageEditParameters.SupportedOptions, want.SupportedOptions) || profile.ImageEditParameters.MaxImages != want.MaxImages {
			t.Errorf("%s image edit parameters=%+v operation=%v", profile.Type, profile.ImageEditParameters, slices.Contains(profile.Operations, "image_edit"))
		}
	}
}

func TestManagedProviderCapabilityProfilesExposeValidatedImageVariationOptions(t *testing.T) {
	all := []string{"n", "response_format", "size", "user"}
	expected := map[string][]string{
		"openai": all, "openai-compatible": all, "azure-openai": all,
		"gemini": {"n", "response_format"},
	}
	for _, profile := range ManagedProviderCapabilityProfiles() {
		want, listed := expected[profile.Type]
		if slices.Contains(profile.Operations, "image_variation") != listed || !slices.Equal(profile.ImageVariationParameters.SupportedOptions, want) {
			t.Errorf("%s image variation parameters=%v operation=%v", profile.Type, profile.ImageVariationParameters.SupportedOptions, slices.Contains(profile.Operations, "image_variation"))
		}
	}
}

func TestManagedProviderCapabilityProfilesExposeValidatedAudioTranscriptionOptions(t *testing.T) {
	all := []string{"language", "prompt", "response_format", "temperature", "timestamp_granularities", "include", "languages", "keywords", "chunking_strategy", "known_speakers"}
	streaming := append(append([]string(nil), all...), "stream")
	expected := map[string][]string{
		"openai": streaming, "openai-compatible": streaming, "azure-openai": streaming,
		"openrouter": {"language", "prompt", "response_format", "temperature", "timestamp_granularities"},
		"gemini":     {"language", "prompt", "response_format", "temperature", "timestamp_granularities", "languages", "keywords", "mode"},
		"mistral":    {"language", "response_format", "temperature", "timestamp_granularities", "keywords"},
		"groq":       {"language", "prompt", "response_format", "temperature", "timestamp_granularities"},
		"together":   {"language", "response_format", "temperature", "timestamp_granularities"},
		"xai":        {"language", "keywords"},
	}
	for _, profile := range ManagedProviderCapabilityProfiles() {
		want, listed := expected[profile.Type]
		if slices.Contains(profile.Operations, "audio_transcription") != listed || !slices.Equal(profile.AudioTranscriptionParameters.SupportedOptions, want) {
			t.Errorf("%s audio transcription parameters=%v operation=%v", profile.Type, profile.AudioTranscriptionParameters.SupportedOptions, slices.Contains(profile.Operations, "audio_transcription"))
		}
	}
}

func TestManagedProviderCapabilityProfilesExposeValidatedAudioTranslationOptions(t *testing.T) {
	expected := map[string][]string{
		"openai": {"prompt", "response_format", "temperature"}, "openai-compatible": {"prompt", "response_format", "temperature"}, "azure-openai": {"prompt", "response_format", "temperature"},
		"gemini":   {"language", "prompt", "response_format", "temperature"},
		"mistral":  {"prompt", "response_format", "temperature"},
		"groq":     {"language", "prompt", "response_format", "temperature"},
		"together": {"prompt", "response_format", "temperature"},
	}
	for _, profile := range ManagedProviderCapabilityProfiles() {
		want, listed := expected[profile.Type]
		if slices.Contains(profile.Operations, "audio_translation") != listed || !slices.Equal(profile.AudioTranslationParameters.SupportedOptions, want) {
			t.Errorf("%s audio translation parameters=%v operation=%v", profile.Type, profile.AudioTranslationParameters.SupportedOptions, slices.Contains(profile.Operations, "audio_translation"))
		}
	}
}

func TestManagedProviderCapabilityProfilesExposeValidatedAudioSpeechOptions(t *testing.T) {
	expected := map[string][]string{
		"openai": {"instructions", "response_format", "speed", "stream_format"}, "openai-compatible": {"instructions", "response_format", "speed", "stream_format"}, "azure-openai": {"instructions", "response_format", "speed", "stream_format"},
		"openrouter": {"response_format", "speed"},
		"gemini":     {"instructions", "response_format", "stream_format"},
		"mistral":    {"response_format"},
		"groq":       {"response_format", "speed"},
		"together":   {"language", "response_format", "stream_format"},
		"xai":        {"language", "response_format", "speed", "stream_format"},
	}
	streaming := map[string]bool{"openai": true, "openai-compatible": true, "azure-openai": true}
	for _, profile := range ManagedProviderCapabilityProfiles() {
		want, listed := expected[profile.Type]
		if slices.Contains(profile.Operations, "audio_speech") != listed || !slices.Equal(profile.AudioSpeechParameters.SupportedOptions, want) || profile.AudioSpeechParameters.SSESupported != streaming[profile.Type] {
			t.Errorf("%s audio speech parameters=%v operation=%v", profile.Type, profile.AudioSpeechParameters.SupportedOptions, slices.Contains(profile.Operations, "audio_speech"))
		}
	}
}

func TestManagedProviderCapabilityProfilesExposeValidatedOCROptions(t *testing.T) {
	allOptions := []string{"pages", "include_image_base64", "image_limit", "image_min_size", "table_format", "extract_header", "extract_footer", "include_blocks", "confidence_scores_granularity", "document_annotation_format", "document_annotation_prompt", "bbox_annotation_format"}
	allForms := []string{"https_document", "inline_document", "https_image", "inline_image"}
	expected := map[string]ProviderOCRParameterPolicy{
		"mistral": {SupportedOptions: allOptions, DocumentForms: allForms},
		"gemini":  {SupportedOptions: []string{"pages", "table_format"}, DocumentForms: []string{"inline_document", "inline_image"}},
	}
	for _, profile := range ManagedProviderCapabilityProfiles() {
		want, listed := expected[profile.Type]
		if slices.Contains(profile.Operations, "ocr") != listed || !slices.Equal(profile.OCRParameters.SupportedOptions, want.SupportedOptions) || !slices.Equal(profile.OCRParameters.DocumentForms, want.DocumentForms) {
			t.Errorf("%s OCR parameters=%+v operation=%v", profile.Type, profile.OCRParameters, slices.Contains(profile.Operations, "ocr"))
		}
	}
}

func TestManagedProviderCapabilityProfilesExposeValidatedVideoCreateOptions(t *testing.T) {
	type videoPolicy struct {
		options    []string
		seconds    []string
		sizes      []string
		references []string
	}
	allOptions := []string{"seconds", "size", "input_reference"}
	allSeconds := []string{"4", "8", "12"}
	allSizes := []string{"720x1280", "1280x720", "1024x1792", "1792x1024"}
	expected := map[string]videoPolicy{
		"openai":            {allOptions, allSeconds, allSizes, []string{"image_url"}},
		"openai-compatible": {allOptions, allSeconds, allSizes, []string{"image_url"}},
		"xai":               {allOptions, allSeconds, []string{"720x1280", "1280x720"}, []string{"image_url"}},
	}
	for _, profile := range ManagedProviderCapabilityProfiles() {
		want, listed := expected[profile.Type]
		got := profile.VideoCreateParameters
		if slices.Contains(profile.Operations, "video") != listed || !slices.Equal(got.SupportedOptions, want.options) || !slices.Equal(got.Seconds, want.seconds) || !slices.Equal(got.Sizes, want.sizes) || !slices.Equal(got.InputReferenceForms, want.references) {
			t.Errorf("%s video create parameters=%+v operation=%v", profile.Type, got, slices.Contains(profile.Operations, "video"))
		}
	}
}

func TestManagedProviderCapabilityProfilesExposeValidatedVideoExtendOptions(t *testing.T) {
	for _, profile := range ManagedProviderCapabilityProfiles() {
		wantOptions, wantSeconds := []string(nil), []string(nil)
		listed := profile.Type == "xai"
		if listed {
			wantOptions = []string{"seconds"}
			wantSeconds = []string{"4", "8", "12"}
		}
		got := profile.VideoExtendParameters
		if slices.Contains(profile.Operations, "video_extension") != listed || !slices.Equal(got.SupportedOptions, wantOptions) || !slices.Equal(got.Seconds, wantSeconds) {
			t.Errorf("%s video extend parameters=%+v operation=%v", profile.Type, got, slices.Contains(profile.Operations, "video_extension"))
		}
	}
}

func TestManagedProviderCapabilityProfilesExposeValidatedFineTuningCreateOptions(t *testing.T) {
	all := []string{"validation_file", "suffix", "seed", "metadata", "method"}
	expected := map[string][]string{"openai": all, "openai-compatible": all}
	for _, profile := range ManagedProviderCapabilityProfiles() {
		want, listed := expected[profile.Type]
		if slices.Contains(profile.Operations, "fine_tuning") != listed || !slices.Equal(profile.FineTuningCreateParameters.SupportedOptions, want) {
			t.Errorf("%s fine-tuning create parameters=%v operation=%v", profile.Type, profile.FineTuningCreateParameters.SupportedOptions, slices.Contains(profile.Operations, "fine_tuning"))
		}
	}
}

func TestManagedProviderCapabilityProfilesExposeValidatedContainerCreateOptions(t *testing.T) {
	all := []string{"expires_after", "memory_limit", "network_policy", "file_ids"}
	expected := map[string][]string{"openai": all, "openai-compatible": all}
	for _, profile := range ManagedProviderCapabilityProfiles() {
		want, listed := expected[profile.Type]
		if slices.Contains(profile.Operations, "container") != listed || !slices.Equal(profile.ContainerCreateParameters.SupportedOptions, want) {
			t.Errorf("%s container create parameters=%v operation=%v", profile.Type, profile.ContainerCreateParameters.SupportedOptions, slices.Contains(profile.Operations, "container"))
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
