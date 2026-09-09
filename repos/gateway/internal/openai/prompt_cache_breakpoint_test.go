package openai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChatPromptCacheBreakpoints(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		count int
		valid bool
	}{
		{"absent", `[{"role":"user","content":"hello"}]`, 0, true},
		{"four", `[{"role":"user","content":[{"type":"text","text":"1","prompt_cache_breakpoint":{"mode":"explicit"}},{"type":"text","text":"2","prompt_cache_breakpoint":{"mode":"explicit"}},{"type":"text","text":"3","prompt_cache_breakpoint":{"mode":"explicit"}},{"type":"text","text":"4","prompt_cache_breakpoint":{"mode":"explicit"}}]}]`, 4, true},
		{"ttl", `[{"role":"user","content":[{"type":"text","text":"1","prompt_cache_breakpoint":{"mode":"explicit","ttl":"1h"}}]}]`, 1, true},
		{"five", `[{"role":"user","content":[{"type":"text","text":"1","prompt_cache_breakpoint":{"mode":"explicit"}},{"type":"text","text":"2","prompt_cache_breakpoint":{"mode":"explicit"}},{"type":"text","text":"3","prompt_cache_breakpoint":{"mode":"explicit"}},{"type":"text","text":"4","prompt_cache_breakpoint":{"mode":"explicit"}},{"type":"text","text":"5","prompt_cache_breakpoint":{"mode":"explicit"}}]}]`, 0, false},
		{"wrong part", `[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"},"prompt_cache_breakpoint":{"mode":"explicit"}}]}]`, 0, false},
		{"wrong mode", `[{"role":"user","content":[{"type":"text","text":"x","prompt_cache_breakpoint":{"mode":"implicit"}}]}]`, 0, false},
		{"extra field", `[{"role":"user","content":[{"type":"text","text":"x","prompt_cache_breakpoint":{"mode":"explicit","extra":true}}]}]`, 0, false},
		{"wrong ttl", `[{"role":"user","content":[{"type":"text","text":"x","prompt_cache_breakpoint":{"mode":"explicit","ttl":"30m"}}]}]`, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var messages []Message
			if err := json.Unmarshal([]byte(tc.body), &messages); err != nil {
				t.Fatal(err)
			}
			count, message := ChatPromptCacheBreakpoints(messages)
			if (message == "") != tc.valid || count != tc.count {
				t.Fatalf("count=%d message=%q", count, message)
			}
		})
	}
}

func TestChatRequestPromptCacheBreakpointsIncludesTools(t *testing.T) {
	request := ChatCompletionRequest{
		Messages: []Message{{Role: "user", Content: []any{map[string]any{"type": "text", "text": "one", "prompt_cache_breakpoint": map[string]any{"mode": "explicit"}}}}},
		Tools:    []Tool{{Type: "function", Function: FunctionDefinition{Name: "lookup", PromptCacheBreakpoint: &PromptCacheBreakpoint{Mode: "explicit", TTL: "1h"}}}},
	}
	if count, message := ChatRequestPromptCacheBreakpoints(request); count != 2 || message != "" {
		t.Fatalf("count=%d message=%q", count, message)
	}
	request.Tools[0].Function.PromptCacheBreakpoint.TTL = "30m"
	if _, message := ChatRequestPromptCacheBreakpoints(request); message == "" {
		t.Fatal("invalid tool breakpoint accepted")
	}
}

func TestTextTransformPreservesPromptCacheBreakpoint(t *testing.T) {
	content := []any{map[string]any{"type": "text", "text": "hello", "prompt_cache_breakpoint": map[string]any{"mode": "explicit"}}}
	transformed := TransformTextContent(content, strings.ToUpper).([]any)[0].(map[string]any)
	if transformed["text"] != "HELLO" || transformed["prompt_cache_breakpoint"].(map[string]any)["mode"] != "explicit" {
		t.Fatalf("breakpoint changed during text transformation: %+v", transformed)
	}
}
