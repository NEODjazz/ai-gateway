package provider

import (
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestPromptCacheRequiresExplicitDeploymentCapability(t *testing.T) {
	request := openai.ChatCompletionRequest{Messages: []openai.Message{
		{Role: "user", Content: []any{
			map[string]any{"type": "text", "text": "hello", "prompt_cache_breakpoint": map[string]any{"mode": "explicit"}},
		}},
	}}
	if got := strings.Join(requiredChatCapabilities(request, true), ","); got != "chat,stream,prompt_cache" {
		t.Fatalf("unexpected required capabilities: %s", got)
	}
	endpoint := Endpoint{Capabilities: []string{"chat", "stream"}}
	if endpoint.supportsCapabilities(requiredChatCapabilities(request, true)...) {
		t.Fatal("deployment without prompt_cache capability accepted")
	}
	endpoint.Capabilities = append(endpoint.Capabilities, "prompt_cache")
	if !endpoint.supportsCapabilities(requiredChatCapabilities(request, true)...) {
		t.Fatal("declared prompt_cache capability rejected")
	}
}

func TestBedrockInvokeRequiresExplicitDeploymentCapability(t *testing.T) {
	request := openai.ChatCompletionRequest{BedrockInvoke: true}
	if got := strings.Join(requiredChatCapabilities(request, false), ","); got != "chat,bedrock_invoke" {
		t.Fatalf("required capabilities=%q", got)
	}
	endpoint := Endpoint{Capabilities: []string{"chat"}}
	if endpoint.supportsCapabilities(requiredChatCapabilities(request, false)...) {
		t.Fatal("deployment without bedrock_invoke capability accepted")
	}
	endpoint.Capabilities = append(endpoint.Capabilities, "bedrock_invoke")
	if !endpoint.supportsCapabilities(requiredChatCapabilities(request, false)...) {
		t.Fatal("declared bedrock_invoke capability rejected")
	}
}

func TestBedrockInvokeStructuredOutputRequiresBothCapabilities(t *testing.T) {
	request := openai.ChatCompletionRequest{BedrockInvoke: true, ResponseFormat: &openai.ResponseFormat{Type: "json_schema"}}
	required := requiredChatCapabilities(request, false)
	if got := strings.Join(required, ","); got != "chat,bedrock_invoke,structured_output" {
		t.Fatalf("required capabilities=%q", got)
	}
	endpoint := Endpoint{Capabilities: []string{"chat", "bedrock_invoke"}}
	if endpoint.supportsCapabilities(required...) {
		t.Fatal("InvokeModel deployment without structured_output capability accepted")
	}
	endpoint.Capabilities = append(endpoint.Capabilities, "structured_output")
	if !endpoint.supportsCapabilities(required...) {
		t.Fatal("InvokeModel deployment with both feature capabilities rejected")
	}
}

func TestNativeIngressRequiresExplicitCapabilities(t *testing.T) {
	request := openai.ChatCompletionRequest{GeminiSafetySettings: []openai.GeminiSafetySetting{{Category: "HARM_CATEGORY_HARASSMENT", Threshold: "BLOCK_ONLY_HIGH"}}}
	if got := strings.Join(requiredChatCapabilities(request, false), ","); got != "chat,gemini_safety_settings" {
		t.Fatalf("required capabilities=%q", got)
	}
	endpoint := Endpoint{Capabilities: []string{"chat"}}
	if endpoint.supportsCapabilities(requiredChatCapabilities(request, false)...) {
		t.Fatal("deployment without native safety capability accepted")
	}
	endpoint.Capabilities = append(endpoint.Capabilities, "gemini_safety_settings")
	if !endpoint.supportsCapabilities(requiredChatCapabilities(request, false)...) {
		t.Fatal("deployment with native safety capability rejected")
	}
}

func TestInlineAudioRequiresExplicitCapability(t *testing.T) {
	request := openai.ChatCompletionRequest{
		Messages: []openai.Message{
			{Role: "user", Content: []any{map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "UklGRgAAAABXQVZF", "format": "wav"}}}},
		},
	}
	if got := strings.Join(requiredChatCapabilities(request, false), ","); got != "chat,audio_input" {
		t.Fatalf("required capabilities=%q", got)
	}
	if (Endpoint{Capabilities: []string{"chat"}}).supportsCapabilities(requiredChatCapabilities(request, false)...) {
		t.Fatal("deployment without audio input capability accepted")
	}
	if !(Endpoint{Capabilities: []string{"chat", "audio_input"}}).supportsCapabilities(requiredChatCapabilities(request, false)...) {
		t.Fatal("deployment with audio input capability rejected")
	}
}

func TestInlineFileRequiresExplicitCapabilityAndBypassesCaches(t *testing.T) {
	request := openai.ChatCompletionRequest{
		Messages: []openai.Message{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "input_file", "file_data": "data:application/pdf;base64,JVBERi0xLjcKY29udGVudA==", "filename": "report.pdf"},
				},
			},
		},
	}
	if got := strings.Join(requiredChatCapabilities(request, false), ","); got != "chat,file_input" {
		t.Fatalf("required capabilities=%s", got)
	}
	if (Endpoint{Capabilities: []string{"chat"}}).supportsCapabilities(requiredChatCapabilities(request, false)...) {
		t.Fatal("deployment without file input capability accepted")
	}
	if !(Endpoint{Capabilities: []string{"chat", "file_input"}}).supportsCapabilities(requiredChatCapabilities(request, false)...) {
		t.Fatal("deployment with file input capability rejected")
	}
	requestContext := modules.RequestContext{CredentialID: "credential", Request: request}
	if providerCacheKey("chat", requestContext) != "" {
		t.Fatal("exact cache enabled for inline file")
	}
	if _, _, ok := semanticRequest(requestContext, Endpoint{Name: "endpoint"}); ok {
		t.Fatal("semantic cache enabled for inline file")
	}
}
