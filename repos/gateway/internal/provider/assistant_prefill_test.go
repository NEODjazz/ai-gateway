package provider

import (
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestAssistantPrefillRequiresExplicitCapability(t *testing.T) {
	prefix := true
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{
		{Role: "user", Content: "question"},
		{Role: "assistant", Content: "answer", Prefix: &prefix},
	}}
	required := requiredChatCapabilities(request, true)
	if got := strings.Join(required, ","); got != "chat,stream,assistant_prefill" {
		t.Fatalf("required capabilities=%q", got)
	}
	without := Endpoint{Capabilities: []string{"chat", "stream"}}
	with := Endpoint{Capabilities: []string{"chat", "stream", "assistant_prefill"}}
	if without.supportsCapabilities(required...) || !with.supportsCapabilities(required...) {
		t.Fatal("assistant prefill capability was not enforced")
	}
}
