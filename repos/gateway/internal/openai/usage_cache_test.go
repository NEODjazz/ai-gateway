package openai

import (
	"encoding/json"
	"testing"
)

func TestUsageDecodesTopLevelPromptCacheHits(t *testing.T) {
	var usage Usage
	if err := json.Unmarshal([]byte(`{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14,"prompt_cache_hit_tokens":7,"prompt_cache_miss_tokens":4}`), &usage); err != nil {
		t.Fatal(err)
	}
	if usage.PromptTokens != 11 || usage.CompletionTokens != 3 || usage.TotalTokens != 14 || usage.PromptTokensDetails == nil || usage.PromptTokensDetails.CachedTokens != 7 {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestUsageTopLevelCacheHitsOverrideDuplicateDetail(t *testing.T) {
	var usage Usage
	if err := json.Unmarshal([]byte(`{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6,"prompt_tokens_details":{"cached_tokens":2},"prompt_cache_hit_tokens":3}`), &usage); err != nil {
		t.Fatal(err)
	}
	if usage.PromptTokensDetails == nil || usage.PromptTokensDetails.CachedTokens != 3 {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestUsageMarshalKeepsCompatibleShape(t *testing.T) {
	payload, err := json.Marshal(Usage{PromptTokens: 5, CompletionTokens: 1, TotalTokens: 6, PromptTokensDetails: &PromptTokenDetails{CachedTokens: 3}})
	if err != nil || string(payload) != `{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6,"prompt_tokens_details":{"cached_tokens":3}}` {
		t.Fatalf("payload=%s err=%v", payload, err)
	}
}
