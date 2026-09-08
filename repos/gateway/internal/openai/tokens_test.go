package openai

import (
	"strings"
	"testing"
)

func TestTokenEstimatesIncludeFullContextAndEquivalentLimits(t *testing.T) {
	n := 10000
	base := ChatCompletionRequest{Messages: []Message{{Role: "user", Content: "test"}}}
	legacy, modern := base, base
	legacy.MaxTokens = &n
	modern.MaxCompletionTokens = &n
	if ReserveTokens(ChatInputTokens(legacy), ChatOutputLimit(legacy)) != ReserveTokens(ChatInputTokens(modern), ChatOutputLimit(modern)) {
		t.Fatal("output limit aliases differ")
	}
	withTools := base
	withTools.Tools = []Tool{{Type: "function", Function: FunctionDefinition{Name: "tool", Parameters: map[string]any{"description": strings.Repeat("schema", 1000)}}}}
	if ChatInputTokens(withTools) <= ChatInputTokens(base)+1000 {
		t.Fatal("tool schema was omitted")
	}
	withArguments := base
	withArguments.Messages = []Message{{Role: "assistant", ToolCalls: []ToolCall{{Function: FunctionCall{Name: "tool", Arguments: strings.Repeat("arg", 1000)}}}}}
	if ChatInputTokens(withArguments) <= ChatInputTokens(base)+500 {
		t.Fatal("tool arguments omitted")
	}
	response := ResponseRequest{Input: "test", Instructions: strings.Repeat("system", 1000)}
	if ResponseInputTokens(response) < 1000 {
		t.Fatal("instructions omitted")
	}
	compact := ResponseCompactRequest{Input: "test", Instructions: strings.Repeat("compact", 1000)}
	if ResponseCompactInputTokens(compact) < 1000 {
		t.Fatal("compaction instructions omitted")
	}
	maxTokens, bestOf := 100, 3
	completion := CompletionRequest{Prompt: "test", MaxTokens: &maxTokens, BestOf: &bestOf}
	if CompletionReserveTokens(completion) != CompletionInputTokens(completion)+300 {
		t.Fatal("completion best_of reserve omitted generated candidates")
	}
	multiPrompt := CompletionRequest{Prompt: []any{[]any{10.0, 11.0}, []any{12.0}}, MaxTokens: &maxTokens, BestOf: &bestOf}
	if CompletionInputTokens(multiPrompt) != 3 || CompletionReserveTokens(multiPrompt) != 603 {
		t.Fatalf("multi-prompt token accounting is incorrect: input=%d reserve=%d", CompletionInputTokens(multiPrompt), CompletionReserveTokens(multiPrompt))
	}
	zero := 0
	completion.MaxTokens, completion.BestOf = &zero, nil
	if CompletionReserveTokens(completion) != CompletionInputTokens(completion) {
		t.Fatal("explicit zero completion output was replaced by a default reserve")
	}
	maxTokens = int(^uint(0) >> 1)
	completion.MaxTokens, completion.BestOf = &maxTokens, &bestOf
	if CompletionReserveTokens(completion) != int(^uint(0)>>1) {
		t.Fatal("completion reserve overflow")
	}
	if ReserveTokens(20, 0) != 20+DefaultOutputTokenReserve {
		t.Fatal("missing default reserve")
	}
	if ReserveTokens(20, int(^uint(0)>>1)) != int(^uint(0)>>1) {
		t.Fatal("overflow")
	}
}

func TestImageEstimationDoesNotTokenizeBase64(t *testing.T) {
	makeInput := func(n int) any {
		return []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + strings.Repeat("a", n)}}}
	}
	if EstimateContextTokens(makeInput(10)) != EstimateContextTokens(makeInput(100000)) {
		t.Fatal("base64 counted as text")
	}
	if EstimateContextTokens(makeInput(10)) < 4096 {
		t.Fatal("image allowance omitted")
	}
}
