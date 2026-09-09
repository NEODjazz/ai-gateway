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
