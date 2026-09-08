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
