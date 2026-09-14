package provider

import (
	"testing"

	"ai-gateway-gateway/internal/modelcatalog"
)

func TestCapabilityContractIsSharedByDeploymentsAndOnboarding(t *testing.T) {
	capabilities := []string{
		"chat", "responses", "interactions", "embeddings", "rerank", "moderation",
		"image_generation", "image_edit", "image_variation",
		"audio_transcription", "audio_translation", "audio_speech", "ocr", "search", "skills", "fine_tuning", "video", "video_remix", "video_extension", "container", "container_files", "container_network", "cached_content", "sandbox", "realtime",
		"stream", "tools", "custom_tools", "response_image_generation", "response_computer", "structured_output", "mcp", "vision",
		"web_search", "web_fetch", "tool_search", "memory_tool", "bash_tool", "text_editor_tool", "computer_toolset", "browser_toolset", "thinking", "zero_output", "inference_geo", "context_management", "tool_result_error", "document_citations", "document_metadata", "document_text", "audio", "audio_input", "video_input", "prompt_cache", "assistant_prefill", "background_responses", "background_interactions", "file_input", "bedrock_invoke", "interaction_agents", "interaction_environment_reuse", "gemini_safety_settings", "gemini_code_execution", "url_context", "google_maps",
	}
	if !validDeploymentCapabilities(capabilities) {
		t.Fatal("deployment rejected a supported model capability")
	}
	catalog := modelcatalog.Catalog{Version: "all-capabilities", Models: []modelcatalog.Model{{Provider: "managed", Model: "model", Capabilities: capabilities}}}
	if _, err := normalizeOnboardingCatalog(catalog); err != nil {
		t.Fatalf("onboarding rejected a supported model capability: %v", err)
	}
	for _, capability := range capabilities {
		if !ValidModelCapability(capability) {
			t.Fatalf("capability %q is missing from the shared contract", capability)
		}
	}
}

func TestCachedContentRequiresChat(t *testing.T) {
	if validDeploymentCapabilities([]string{"cached_content"}) || !validDeploymentCapabilities([]string{"chat", "cached_content"}) {
		t.Fatal("cached content capability dependency is incorrect")
	}
}

func TestWebSearchCapabilitySupportsChatOrResponses(t *testing.T) {
	if validDeploymentCapabilities([]string{"web_search"}) || !validDeploymentCapabilities([]string{"chat", "web_search"}) || !validDeploymentCapabilities([]string{"responses", "web_search"}) {
		t.Fatal("web search capability dependency is incorrect")
	}
}

func TestCustomToolsCapabilityRequiresResponsesAndTools(t *testing.T) {
	for _, capabilities := range [][]string{{"custom_tools"}, {"responses", "custom_tools"}, {"tools", "custom_tools"}, {"chat", "tools", "custom_tools"}} {
		if validDeploymentCapabilities(capabilities) {
			t.Fatalf("custom tools accepted without Responses and tools: %v", capabilities)
		}
	}
	if !validDeploymentCapabilities([]string{"responses", "tools", "custom_tools"}) {
		t.Fatal("custom tools rejected with Responses and tools")
	}
}

func TestResponseImageGenerationCapabilityRequiresResponsesAndTools(t *testing.T) {
	for _, capabilities := range [][]string{{"response_image_generation"}, {"responses", "response_image_generation"}, {"tools", "response_image_generation"}, {"chat", "tools", "response_image_generation"}} {
		if validDeploymentCapabilities(capabilities) {
			t.Fatalf("Responses image generation accepted without Responses and tools: %v", capabilities)
		}
	}
	if !validDeploymentCapabilities([]string{"responses", "tools", "response_image_generation"}) {
		t.Fatal("Responses image generation rejected with Responses and tools")
	}
}

func TestResponseComputerCapabilityRequiresResponsesAndTools(t *testing.T) {
	for _, capabilities := range [][]string{{"response_computer"}, {"responses", "response_computer"}, {"tools", "response_computer"}, {"chat", "tools", "response_computer"}} {
		if validDeploymentCapabilities(capabilities) {
			t.Fatalf("Responses computer accepted without Responses and tools: %v", capabilities)
		}
	}
	if !validDeploymentCapabilities([]string{"responses", "tools", "response_computer"}) {
		t.Fatal("Responses computer rejected with Responses and tools")
	}
}

func TestGeminiSafetySettingsRequireChat(t *testing.T) {
	if validDeploymentCapabilities([]string{"gemini_safety_settings"}) || !validDeploymentCapabilities([]string{"chat", "gemini_safety_settings"}) {
		t.Fatal("Gemini safety settings capability dependency is incorrect")
	}
}

func TestGeminiCodeExecutionRequiresChat(t *testing.T) {
	if validDeploymentCapabilities([]string{"gemini_code_execution"}) || !validDeploymentCapabilities([]string{"chat", "gemini_code_execution"}) {
		t.Fatal("Gemini code execution capability dependency is incorrect")
	}
}

func TestGeminiURLContextRequiresChat(t *testing.T) {
	if validDeploymentCapabilities([]string{"url_context"}) || !validDeploymentCapabilities([]string{"chat", "url_context"}) {
		t.Fatal("Gemini URL context capability dependency is incorrect")
	}
}

func TestGeminiGoogleMapsRequiresChat(t *testing.T) {
	if validDeploymentCapabilities([]string{"google_maps"}) || !validDeploymentCapabilities([]string{"chat", "google_maps"}) {
		t.Fatal("Gemini Google Maps capability dependency is incorrect")
	}
}

func TestThinkingCapabilityRequiresChat(t *testing.T) {
	if validDeploymentCapabilities([]string{"thinking"}) || !validDeploymentCapabilities([]string{"chat", "thinking"}) {
		t.Fatal("thinking capability dependency is incorrect")
	}
}

func TestZeroOutputCapabilityRequiresChat(t *testing.T) {
	if validDeploymentCapabilities([]string{"zero_output"}) || !validDeploymentCapabilities([]string{"chat", "zero_output"}) {
		t.Fatal("zero output capability dependency is incorrect")
	}
}

func TestInferenceGeoCapabilityRequiresChat(t *testing.T) {
	if validDeploymentCapabilities([]string{"inference_geo"}) || !validDeploymentCapabilities([]string{"chat", "inference_geo"}) {
		t.Fatal("inference geography capability dependency is incorrect")
	}
}

func TestContextManagementCapabilityRequiresChat(t *testing.T) {
	if validDeploymentCapabilities([]string{"context_management"}) || !validDeploymentCapabilities([]string{"chat", "context_management"}) {
		t.Fatal("context management capability dependency is incorrect")
	}
}

func TestToolResultErrorCapabilityRequiresChat(t *testing.T) {
	if validDeploymentCapabilities([]string{"tool_result_error"}) || !validDeploymentCapabilities([]string{"chat", "tool_result_error"}) {
		t.Fatal("tool-result error capability dependency is incorrect")
	}
}

func TestDocumentCitationsCapabilityRequiresChat(t *testing.T) {
	if validDeploymentCapabilities([]string{"document_citations"}) || !validDeploymentCapabilities([]string{"chat", "document_citations"}) {
		t.Fatal("document citations capability dependency is incorrect")
	}
}

func TestDocumentMetadataCapabilityRequiresChat(t *testing.T) {
	if validDeploymentCapabilities([]string{"document_metadata"}) || !validDeploymentCapabilities([]string{"chat", "document_metadata"}) {
		t.Fatal("document metadata capability dependency is incorrect")
	}
}

func TestTextDocumentsCapabilityRequiresChat(t *testing.T) {
	if validDeploymentCapabilities([]string{"document_text"}) || !validDeploymentCapabilities([]string{"chat", "document_text"}) {
		t.Fatal("text documents capability dependency is incorrect")
	}
}

func TestInteractionAgentCapabilityRequiresInteractions(t *testing.T) {
	if validDeploymentCapabilities([]string{"interaction_agents"}) {
		t.Fatal("interaction agent capability was accepted without interactions")
	}
	if !validDeploymentCapabilities([]string{"interactions", "interaction_agents"}) {
		t.Fatal("interaction agent capability was rejected with interactions")
	}
}

func TestInteractionEnvironmentReuseRequiresAgentInteractions(t *testing.T) {
	for _, capabilities := range [][]string{{"interaction_environment_reuse"}, {"interactions", "interaction_environment_reuse"}} {
		if validDeploymentCapabilities(capabilities) {
			t.Fatalf("environment reuse accepted without agent interactions: %v", capabilities)
		}
	}
	if !validDeploymentCapabilities([]string{"interactions", "interaction_agents", "interaction_environment_reuse"}) {
		t.Fatal("environment reuse rejected with agent interactions")
	}
}

func TestBackgroundInteractionCapabilityRequiresInteractions(t *testing.T) {
	if validDeploymentCapabilities([]string{"background_interactions"}) {
		t.Fatal("background interaction capability was accepted without interactions")
	}
	if !validDeploymentCapabilities([]string{"interactions", "background_interactions"}) {
		t.Fatal("background interaction capability was rejected with interactions")
	}
}

func TestVideoExtensionCapabilityRequiresVideo(t *testing.T) {
	for _, capability := range []string{"video_remix", "video_extension"} {
		if validDeploymentCapabilities([]string{capability}) {
			t.Fatalf("%s was accepted without video lifecycle capability", capability)
		}
	}
	if !validDeploymentCapabilities([]string{"video", "video_remix", "video_extension"}) {
		t.Fatal("video sub-operation was rejected with its required video capability")
	}
}

func TestContainerFilesCapabilityRequiresContainer(t *testing.T) {
	if validDeploymentCapabilities([]string{"container_files"}) || !validDeploymentCapabilities([]string{"container", "container_files"}) {
		t.Fatal("container files capability dependency is incorrect")
	}
}

func TestContainerNetworkCapabilityRequiresContainer(t *testing.T) {
	if validDeploymentCapabilities([]string{"container_network"}) || !validDeploymentCapabilities([]string{"container", "container_network"}) {
		t.Fatal("container network capability dependency is incorrect")
	}
}

func TestCapabilityContractRejectsUnknownAndDuplicates(t *testing.T) {
	if ValidModelCapability("unknown") || validDeploymentCapabilities([]string{"chat", "chat"}) {
		t.Fatal("invalid capability set accepted")
	}
	catalog := modelcatalog.Catalog{Version: "invalid", Models: []modelcatalog.Model{{Provider: "managed", Model: "model", Capabilities: []string{"unknown"}}}}
	if _, err := normalizeOnboardingCatalog(catalog); err == nil {
		t.Fatal("onboarding accepted an unknown capability")
	}
}
