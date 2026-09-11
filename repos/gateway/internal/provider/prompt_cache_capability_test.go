package provider

import (
	"strings"
	"testing"

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
