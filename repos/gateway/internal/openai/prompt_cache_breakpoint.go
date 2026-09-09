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
			if !ok || len(value) != 1 || value["mode"] != "explicit" {
				return 0, "prompt_cache_breakpoint.mode must be explicit"
			}
			count++
			if count > 4 {
				return 0, "at most 4 prompt_cache_breakpoint values are allowed"
			}
		}
	}
	return count, ""
}
