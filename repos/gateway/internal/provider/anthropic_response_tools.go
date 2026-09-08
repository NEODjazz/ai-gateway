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

func appendAnthropicResponseMessage(messages []anthropicMessage, message anthropicMessage) []anthropicMessage {
	if len(messages) == 0 || messages[len(messages)-1].Role != message.Role {
		return append(messages, message)
	}
	previous := &messages[len(messages)-1]
	previous.Content = append(anthropicResponseBlocks(previous.Content), anthropicResponseBlocks(message.Content)...)
	return messages
}

func anthropicResponseBlocks(content any) []anthropicContent {
	if blocks, ok := content.([]anthropicContent); ok {
		return blocks
	}
	if text, ok := content.(string); ok && text != "" {
		return []anthropicContent{{Type: "text", Text: text}}
	}
	return nil
}

// The native protocol requires tool results before other user content.
func orderAnthropicToolResults(messages []anthropicMessage) []anthropicMessage {
	for i := range messages {
		if messages[i].Role != "user" {
			continue
		}
		blocks, ok := messages[i].Content.([]anthropicContent)
		if !ok {
			continue
		}
		hasResult := false
		for _, block := range blocks {
			if block.Type == "tool_result" {
				hasResult = true
				break
			}
		}
		if !hasResult {
			continue
		}
		ordered := make([]anthropicContent, 0, len(blocks))
		for _, block := range blocks {
			if block.Type == "tool_result" {
				ordered = append(ordered, block)
			}
		}
		for _, block := range blocks {
			if block.Type != "tool_result" {
				ordered = append(ordered, block)
			}
		}
		messages[i].Content = ordered
	}
	return messages
}
