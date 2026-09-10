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
	choices := 3
	modern.N = &choices
	if ChatOutputReserve(modern) != 30000 || ChatReserveTokens(modern) != ChatInputTokens(modern)+30000 {
		t.Fatal("chat choices were omitted from output reserve")
	}
	modern.MaxCompletionTokens = nil
	if ChatOutputReserve(modern) != DefaultOutputTokenReserve*choices {
		t.Fatal("default output reserve was not applied to every choice")
	}
	maximum := int(^uint(0) >> 1)
	modern.MaxCompletionTokens = &maximum
	if ChatOutputReserve(modern) != maximum || ChatReserveTokens(modern) != maximum {
		t.Fatal("chat choice reserve overflow")
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
	refusal := strings.Repeat("refusal", 1000)
	withRefusal := base
	withRefusal.Messages = []Message{{Role: "assistant", Refusal: &refusal}}
	if ChatInputTokens(withRefusal) <= ChatInputTokens(base)+1000 {
		t.Fatal("assistant refusal omitted")
	}
	withAudio := base
	withAudio.Messages = []Message{{Role: "assistant", Audio: &ChatAudio{ID: strings.Repeat("audio", 100)}}}
	if ChatInputTokens(withAudio) <= ChatInputTokens(base)+100 {
		t.Fatal("assistant audio reference omitted")
	}
	withReasoning := base
	withReasoning.Messages = []Message{{Role: "assistant", ReasoningContent: strings.Repeat("reasoning", 1000)}}
	if ChatInputTokens(withReasoning) <= ChatInputTokens(base)+1000 {
		t.Fatal("assistant reasoning_content omitted")
	}
	maximumFetches := 3
	withFetch := base
	withFetch.WebFetchOptions = &ChatWebFetchOptions{AllowedDomains: []string{"docs.example.com"}, MaxUses: &maximumFetches, MaxContentTokens: 20000}
	if ChatInputTokens(withFetch) <= ChatInputTokens(base) {
		t.Fatal("web fetch configuration was omitted from the input-token reserve")
	}
	withSearch := base
	withSearch.WebSearchOptions = &ChatWebSearchOptions{SearchContextSize: "medium"}
	if ChatInputTokens(withSearch) <= ChatInputTokens(base) {
		t.Fatal("web search configuration was omitted from the input-token reserve")
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
