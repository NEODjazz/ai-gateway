package openai

// ChatPromptCacheBreakpoints validates explicit breakpoints in Chat text parts
// and returns how many were supplied.
func ChatPromptCacheBreakpoints(messages []Message) (int, string) {
	count := 0
	for _, message := range messages {
		parts, ok := message.Content.([]any)
		if !ok {
			continue
		}
		for _, item := range parts {
			part, ok := item.(map[string]any)
			if !ok {
				continue
			}
			breakpoint, supplied := part["prompt_cache_breakpoint"]
			if !supplied {
				continue
			}
			if part["type"] != "text" {
				return 0, "prompt_cache_breakpoint is only supported on Chat text parts"
			}
			value, ok := breakpoint.(map[string]any)
			if !ok || !validPromptCacheBreakpointMap(value) {
				return 0, "prompt_cache_breakpoint requires mode=explicit and optional ttl=5m or 1h"
			}
			count++
			if count > 4 {
				return 0, "at most 4 prompt_cache_breakpoint values are allowed"
			}
		}
	}
	return count, ""
}

func ChatRequestPromptCacheBreakpoints(request ChatCompletionRequest) (int, string) {
	count, message := ChatPromptCacheBreakpoints(request.Messages)
	if message != "" {
		return 0, message
	}
	if request.AnthropicCacheControl != nil {
		if !ValidPromptCacheBreakpoint(request.AnthropicCacheControl) {
			return 0, "top-level prompt cache control requires mode=explicit and optional ttl=5m or 1h"
		}
		count++
		if count > 4 {
			return 0, "at most 4 prompt_cache_breakpoint values are allowed"
		}
	}
	for _, tool := range request.Tools {
		if tool.Function.PromptCacheBreakpoint == nil {
			continue
		}
		if !ValidPromptCacheBreakpoint(tool.Function.PromptCacheBreakpoint) {
			return 0, "tool prompt_cache_breakpoint requires mode=explicit and optional ttl=5m or 1h"
		}
		count++
		if count > 4 {
			return 0, "at most 4 prompt_cache_breakpoint values are allowed"
		}
	}
	for _, tool := range request.AnthropicClientTools {
		if tool.PromptCacheBreakpoint == nil {
			continue
		}
		if !ValidPromptCacheBreakpoint(tool.PromptCacheBreakpoint) {
			return 0, "client tool prompt_cache_breakpoint requires mode=explicit and optional ttl=5m or 1h"
		}
		count++
		if count > 4 {
			return 0, "at most 4 prompt_cache_breakpoint values are allowed"
		}
	}
	for _, toolset := range request.AnthropicClientToolsets {
		if toolset.PromptCacheBreakpoint == nil {
			continue
		}
		if !ValidPromptCacheBreakpoint(toolset.PromptCacheBreakpoint) {
			return 0, "client toolset prompt_cache_breakpoint requires mode=explicit and optional ttl=5m or 1h"
		}
		count++
		if count > 4 {
			return 0, "at most 4 prompt_cache_breakpoint values are allowed"
		}
	}
	return count, ""
}

func ValidPromptCacheBreakpoint(value *PromptCacheBreakpoint) bool {
	return value != nil && value.Mode == "explicit" && (value.TTL == "" || value.TTL == "5m" || value.TTL == "1h")
}

func validPromptCacheBreakpointMap(value map[string]any) bool {
	if len(value) < 1 || len(value) > 2 || value["mode"] != "explicit" {
		return false
	}
	for key := range value {
		if key != "mode" && key != "ttl" {
			return false
		}
	}
	ttl, supplied := value["ttl"]
	return !supplied || ttl == "5m" || ttl == "1h"
}
