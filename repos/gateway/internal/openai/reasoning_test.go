package openai

import (
	"strings"
	"testing"
)

func TestValidateReasoningBlocks(t *testing.T) {
	valid := []ReasoningBlock{{Type: "thinking", Thinking: "plan", Signature: "signed"}, {Type: "redacted_thinking", Data: "opaque"}}
	if err := ValidateReasoningBlocks(valid); err != nil {
		t.Fatal(err)
	}
	for _, blocks := range [][]ReasoningBlock{
		{{Type: "thinking", Thinking: "plan"}},
		{{Type: "redacted_thinking"}},
		{{Type: "unknown", Data: "opaque"}},
		{{Type: "redacted_thinking", Data: strings.Repeat("x", (1<<20)+1)}},
	} {
		if err := ValidateReasoningBlocks(blocks); err == nil {
			t.Fatalf("invalid reasoning blocks accepted: %+v", blocks)
		}
	}
}

func TestValidateChatReasoningContent(t *testing.T) {
	if err := ValidateChatReasoningContent("assistant", strings.Repeat("x", MaxChatReasoningContentBytes)); err != nil {
		t.Fatalf("valid assistant reasoning_content rejected: %v", err)
	}
	if err := ValidateChatReasoningContent("user", "plan"); err == nil {
		t.Fatal("reasoning_content on a user message was accepted")
	}
	if err := ValidateChatReasoningContent("assistant", strings.Repeat("x", MaxChatReasoningContentBytes+1)); err == nil {
		t.Fatal("oversized reasoning_content was accepted")
	}
	if err := ValidateChatReasoningContent("user", ""); err != nil {
		t.Fatalf("empty reasoning_content should be ignored: %v", err)
	}
}
