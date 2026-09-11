package provider

import (
	"testing"

	"ai-gateway-gateway/internal/modelcatalog"
)

func TestCapabilityContractIsSharedByDeploymentsAndOnboarding(t *testing.T) {
	capabilities := []string{
		"chat", "responses", "embeddings", "rerank", "moderation",
		"image_generation", "image_edit", "image_variation",
		"audio_transcription", "audio_translation", "audio_speech", "ocr", "search", "video", "video_remix", "video_extension", "realtime",
		"stream", "tools", "structured_output", "mcp", "vision",
		"web_search", "web_fetch", "audio", "prompt_cache", "assistant_prefill", "background_responses", "file_input", "bedrock_invoke",
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

func TestCapabilityContractRejectsUnknownAndDuplicates(t *testing.T) {
	if ValidModelCapability("unknown") || validDeploymentCapabilities([]string{"chat", "chat"}) {
		t.Fatal("invalid capability set accepted")
	}
	catalog := modelcatalog.Catalog{Version: "invalid", Models: []modelcatalog.Model{{Provider: "managed", Model: "model", Capabilities: []string{"unknown"}}}}
	if _, err := normalizeOnboardingCatalog(catalog); err == nil {
		t.Fatal("onboarding accepted an unknown capability")
	}
}
