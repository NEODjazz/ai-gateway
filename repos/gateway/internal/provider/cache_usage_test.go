package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestCompatibleChatPreservesTopLevelPromptCacheHits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"chat","object":"chat.completion","model":"model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14,"prompt_cache_hit_tokens":7,"prompt_cache_miss_tokens":4}}`)
	}))
	defer server.Close()
	response, err := NewOpenAICompatible(server.URL, "", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
	if err != nil || response.Usage.PromptTokensDetails == nil || response.Usage.PromptTokensDetails.CachedTokens != 7 {
		t.Fatalf("usage=%+v err=%v", response.Usage, err)
	}
}

func TestCompatibleChatStreamPreservesTopLevelPromptCacheHits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat\",\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"}}]}\n\ndata: {\"id\":\"chat\",\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":3,\"total_tokens\":14,\"prompt_cache_hit_tokens\":7,\"prompt_cache_miss_tokens\":4}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	response, err := NewOpenAICompatible(server.URL, "", true).StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, Stream: true}, func(string) error { return nil })
	if err != nil || response.Usage.PromptTokensDetails == nil || response.Usage.PromptTokensDetails.CachedTokens != 7 {
		t.Fatalf("usage=%+v err=%v", response.Usage, err)
	}
}
