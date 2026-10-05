package provider

import (
	"errors"

	"ai-gateway-gateway/internal/openai"
)

func applyResponseReasoningTextEvent(response *openai.ResponseResponse, outputIndex int, event string, decoded map[string]any) error {
	if event != "response.reasoning_text.delta" && event != "response.reasoning_text.done" {
		return nil
	}
	field := "delta"
	if event == "response.reasoning_text.done" {
		field = "text"
	}
	text, err := requiredResponseEventString(decoded, field)
	if err != nil {
		return err
	}
	index, err := boundedResponseStreamIndex(decoded, "content_index", maxResponseStreamContentParts)
	if err != nil {
		return err
	}
	item, err := ensureResponseReasoningItem(response, outputIndex)
	if err != nil {
		return err
	}
	if id, ok := decoded["item_id"].(string); ok {
		item.ID = id
	}
	for len(item.Content) <= index {
		item.Content = append(item.Content, openai.ResponseOutputContent{})
	}
	part := &item.Content[index]
	part.Type = "reasoning_text"
	if event == "response.reasoning_text.delta" {
		part.Text += text
	} else {
		part.Text = text
	}
	return validateResponseOutputContent(*part)
}

func ensureResponseReasoningItem(response *openai.ResponseResponse, index int) (*openai.ResponseOutputItem, error) {
	item := ensureResponseOutputItem(response, index)
	if item.Type != "" && item.Type != "reasoning" {
		// The stream starts with an empty assistant placeholder for providers that
		// omit output_item events. Only that placeholder may change its type.
		if item.Type != "message" || item.ID != "" || len(item.Content) != 1 ||
			item.Content[0].Type != "output_text" || item.Content[0].Text != "" ||
			len(item.Content[0].Annotations) != 0 || len(item.Content[0].Logprobs) != 0 {
			return nil, errors.New("provider returned reasoning event for a non-reasoning output item")
		}
		item.Content = nil
	}
	item.Type, item.Role = "reasoning", ""
	return item, nil
}
