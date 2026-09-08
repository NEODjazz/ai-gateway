package provider

import (
	"encoding/json"
	"errors"
)

func anthropicResponseToolMessage(item map[string]any) (anthropicMessage, error) {
	invalid := func() (anthropicMessage, error) {
		return anthropicMessage{}, &Error{Class: FailureClientRequest, Provider: "anthropic", StatusCode: 400, UpstreamCode: "invalid_request", Param: "input", Err: errors.New("invalid Responses function call history")}
	}
	id, ok := item["call_id"].(string)
	if !ok || id == "" {
		return invalid()
	}
	if item["type"] == "function_call" {
		name, ok := item["name"].(string)
		if !ok || name == "" {
			return invalid()
		}
		arguments, ok := item["arguments"].(string)
		if !ok {
			return invalid()
		}
		var input map[string]json.RawMessage
		if json.Unmarshal([]byte(arguments), &input) != nil || input == nil {
			return invalid()
		}
		return anthropicMessage{Role: "assistant", Content: []anthropicContent{{Type: "tool_use", ID: id, Name: name, Input: input}}}, nil
	}
	output, ok := item["output"]
	if !ok {
		return invalid()
	}
	switch output.(type) {
	case string, []any:
	default:
		return invalid()
	}
	return anthropicMessage{Role: "user", Content: []anthropicContent{{Type: "tool_result", ToolUseID: id, Content: anthropicMessageContent(output)}}}, nil
}

func validateAnthropicResponseHistory(input any) error {
	if items, ok := input.([]any); ok {
		for _, value := range items {
			if item, ok := value.(map[string]any); ok && (item["type"] == "function_call" || item["type"] == "function_call_output") {
				if _, err := anthropicResponseToolMessage(item); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
