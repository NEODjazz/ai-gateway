package provider

import (
	"errors"

	"ai-gateway-gateway/internal/openai"
)

func applyResponseSummaryEvent(response *openai.ResponseResponse, outputIndex int, event string, decoded map[string]any) error {
	var text string
	appendText := false
	switch event {
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		part, ok := decoded["part"].(map[string]any)
		if !ok || part["type"] != "summary_text" {
			return errors.New("invalid Responses summary part")
		}
		text, ok = part["text"].(string)
		if !ok {
			return errors.New("invalid Responses summary text")
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done":
		field := "text"
		appendText = event == "response.reasoning_summary_text.delta"
		if appendText {
			field = "delta"
		}
		var ok bool
		text, ok = decoded[field].(string)
		if !ok {
			return errors.New("invalid Responses summary text")
		}
	default:
		return nil
	}
	index, err := boundedResponseStreamIndex(decoded, "summary_index", maxResponseStreamContentParts)
	if err != nil {
		return err
	}
	item := ensureResponseOutputItem(response, outputIndex)
	item.Type = "reasoning"
	item.Role, item.Content = "", nil
	if id, ok := decoded["item_id"].(string); ok {
		item.ID = id
	}
	for len(item.Summary) <= index {
		item.Summary = append(item.Summary, openai.ResponseOutputContent{})
	}
	part := &item.Summary[index]
	part.Type = "summary_text"
	if appendText {
		part.Text += text
	} else {
		part.Text = text
	}
	return nil
}
