package provider

import (
	"testing"

	"ai-gateway-gateway/internal/modelcatalog"
)

func TestCapabilityContractIsSharedByDeploymentsAndOnboarding(t *testing.T) {
	capabilities := []string{
		"chat", "responses", "interactions", "embeddings", "rerank", "moderation",
		"image_generation", "image_edit", "image_variation",
		"audio_transcription", "audio_translation", "audio_speech", "ocr", "search", "skills", "fine_tuning", "video", "video_remix", "video_extension", "container", "container_files", "container_network", "sandbox", "realtime",
		"stream", "tools", "structured_output", "mcp", "vision",
		"web_search", "web_fetch", "tool_search", "memory_tool", "bash_tool", "text_editor_tool", "audio", "audio_input", "video_input", "prompt_cache", "assistant_prefill", "background_responses", "background_interactions", "file_input", "bedrock_invoke", "interaction_agents", "interaction_environment_reuse", "gemini_safety_settings", "gemini_code_execution",
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
