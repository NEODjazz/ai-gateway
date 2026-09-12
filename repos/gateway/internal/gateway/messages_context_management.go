package gateway

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

func validateMessagesContextManagement(config *messagesContextManagement) (json.RawMessage, error) {
	if config == nil {
		return nil, nil
	}
	if len(config.Edits) == 0 || len(config.Edits) > 2 {
		return nil, errors.New("context_management.edits must contain one or two strategies")
	}
	seen := map[string]bool{}
	validated := make([]any, 0, len(config.Edits))
	for index, raw := range config.Edits {
		var kind struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &kind) != nil || kind.Type == "" {
			return nil, errors.New("invalid context management edit")
		}
		if seen[kind.Type] {
			return nil, errors.New("context management strategies must be unique")
		}
		seen[kind.Type] = true
		switch kind.Type {
		case "clear_tool_uses_20250919":
			var edit messagesClearToolUsesEdit
			if decodeMessagesValue(raw, &edit) != nil || !validContextLimit(edit.Trigger, "input_tokens", "tool_uses") || !validContextLimit(edit.Keep, "tool_uses") || !validContextLimit(edit.ClearAtLeast, "input_tokens") {
				return nil, errors.New("invalid clear_tool_uses context edit")
			}
			if !validContextToolNames(edit.ExcludeTools) || !validClearToolInputs(edit.ClearToolInputs) {
				return nil, errors.New("invalid clear_tool_uses context edit")
			}
			validated = append(validated, edit)
		case "clear_thinking_20251015":
			if index != 0 {
				return nil, errors.New("clear_thinking context edit must be first")
			}
			var edit messagesClearThinkingEdit
			if decodeMessagesValue(raw, &edit) != nil || !validThinkingKeep(edit.Keep) {
				return nil, errors.New("invalid clear_thinking context edit")
			}
			validated = append(validated, edit)
		default:
			return nil, errors.New("unsupported context management edit")
		}
	}
	return json.Marshal(struct {
		Edits []any `json:"edits"`
	}{Edits: validated})
}

func validContextLimit(limit *messagesContextLimit, allowed ...string) bool {
	if limit == nil {
		return true
	}
	if limit.Value <= 0 || limit.Value > 10_000_000 {
		return false
	}
	for _, kind := range allowed {
		if limit.Type == kind {
			return true
		}
	}
	return false
}

func validContextToolNames(names []string) bool {
	if len(names) > 64 {
		return false
	}
	seen := map[string]bool{}
	for _, name := range names {
		if name == "" || utf8.RuneCountInString(name) > 128 || seen[name] {
			return false
		}
		seen[name] = true
	}
	return true
}

func validClearToolInputs(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var enabled bool
	if decodeMessagesValue(raw, &enabled) == nil {
		return true
	}
	var names []string
	return decodeMessagesValue(raw, &names) == nil && validContextToolNames(names)
}

func validThinkingKeep(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var all string
	if decodeMessagesValue(raw, &all) == nil {
		return all == "all"
	}
	var limit messagesContextLimit
	return decodeMessagesValue(raw, &limit) == nil && validContextLimit(&limit, "thinking_turns")
}

func messagesNativeContextManagement(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > 64<<10 {
		return nil, errors.New("invalid native context management result")
	}
	if string(raw) == "null" {
		return nil, nil
	}
	var result struct {
		AppliedEdits []json.RawMessage `json:"applied_edits"`
	}
	if decodeMessagesValue(raw, &result) != nil || len(result.AppliedEdits) > 2 {
		return nil, errors.New("invalid native context management result")
	}
	seen := map[string]bool{}
	for _, rawEdit := range result.AppliedEdits {
		var edit struct {
			Type                 string `json:"type"`
			ClearedInputTokens   *int   `json:"cleared_input_tokens"`
			ClearedToolUses      *int   `json:"cleared_tool_uses,omitempty"`
			ClearedThinkingTurns *int   `json:"cleared_thinking_turns,omitempty"`
		}
		if decodeMessagesValue(rawEdit, &edit) != nil || edit.ClearedInputTokens == nil || *edit.ClearedInputTokens < 0 || seen[edit.Type] {
			return nil, errors.New("invalid native context management result")
		}
		seen[edit.Type] = true
		switch edit.Type {
		case "clear_tool_uses_20250919":
			if edit.ClearedToolUses == nil || *edit.ClearedToolUses < 0 || edit.ClearedThinkingTurns != nil {
				return nil, errors.New("invalid native context management result")
			}
		case "clear_thinking_20251015":
			if edit.ClearedThinkingTurns == nil || *edit.ClearedThinkingTurns < 0 || edit.ClearedToolUses != nil {
				return nil, errors.New("invalid native context management result")
			}
		default:
			return nil, errors.New("invalid native context management result")
		}
	}
	var decoded map[string]any
	if json.Unmarshal(raw, &decoded) != nil {
		return nil, errors.New("invalid native context management result")
	}
	return decoded, nil
}
