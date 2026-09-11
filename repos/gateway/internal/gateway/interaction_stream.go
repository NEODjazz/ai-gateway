package gateway

import (
	"encoding/json"
	"errors"
	"fmt"

	"ai-gateway-gateway/internal/openai"
)

type interactionStreamTransformer struct{}

func newInteractionStreamTransformer() *interactionStreamTransformer {
	return &interactionStreamTransformer{}
}

func (*interactionStreamTransformer) Transform(event, payload string) ([]responseStreamEvent, error) {
	var envelope struct {
		Type        string                    `json:"type"`
		Response    openai.ResponseResponse   `json:"response"`
		Item        openai.ResponseOutputItem `json:"item"`
		OutputIndex int                       `json:"output_index"`
		Delta       string                    `json:"delta"`
	}
	if event == "error" {
		var failure map[string]any
		if json.Unmarshal([]byte(payload), &failure) != nil {
			return nil, errors.New("invalid streamed interaction error")
		}
		failure["event_type"] = "error"
		return interactionEvent("error", failure)
	}
	if json.Unmarshal([]byte(payload), &envelope) != nil || envelope.Type != "" && envelope.Type != event || envelope.OutputIndex < 0 || envelope.OutputIndex > 1023 {
		return nil, errors.New("invalid Responses event for interaction stream")
	}
	switch event {
	case "response.created":
		interaction := openai.InteractionFromResponse(envelope.Response)
		interaction.Status = "in_progress"
		return interactionEvent("interaction.created", map[string]any{"interaction": interaction})
	case "response.in_progress", "response.queued":
		return interactionEvent("interaction.status_update", map[string]any{"interaction_id": envelope.Response.ID, "status": "in_progress"})
	case "response.output_item.added":
		step, err := interactionStreamStep(envelope.Item)
		if err != nil {
			return nil, err
		}
		return interactionEvent("step.start", map[string]any{"index": envelope.OutputIndex, "step": step})
	case "response.output_text.delta":
		return interactionEvent("step.delta", map[string]any{"index": envelope.OutputIndex, "delta": map[string]any{"type": "text", "text": envelope.Delta}})
	case "response.refusal.delta":
		return interactionEvent("step.delta", map[string]any{"index": envelope.OutputIndex, "delta": map[string]any{"type": "text", "text": envelope.Delta}})
	case "response.function_call_arguments.delta":
		return interactionEvent("step.delta", map[string]any{"index": envelope.OutputIndex, "delta": map[string]any{"type": "arguments_delta", "arguments": envelope.Delta}})
	case "response.reasoning_summary_text.delta":
		return interactionEvent("step.delta", map[string]any{"index": envelope.OutputIndex, "delta": map[string]any{"type": "thought_summary", "text": envelope.Delta}})
	case "response.output_item.done":
		return interactionEvent("step.stop", map[string]any{"index": envelope.OutputIndex})
	case "response.completed", "response.incomplete", "response.failed":
		return interactionEvent("interaction.completed", map[string]any{"interaction": openai.InteractionFromResponse(envelope.Response)})
	case "response.content_part.added", "response.content_part.done", "response.output_text.done",
		"response.output_text.annotation.added", "response.refusal.done",
		"response.function_call_arguments.done", "response.reasoning_summary_part.added",
		"response.reasoning_summary_text.done", "response.reasoning_summary_part.done":
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported Responses event %q for interaction stream", event)
	}
}

func interactionStreamStep(item openai.ResponseOutputItem) (openai.InteractionStep, error) {
	switch item.Type {
	case "message":
		return openai.InteractionStep{ID: item.ID, Type: "model_output"}, nil
	case "function_call":
		id := item.CallID
		if id == "" {
			id = item.ID
		}
		return openai.InteractionStep{ID: id, Type: "function_call", Name: item.Name}, nil
	case "reasoning":
		return openai.InteractionStep{ID: item.ID, Type: "thought"}, nil
	default:
		return openai.InteractionStep{}, fmt.Errorf("unsupported response output item %q for interaction stream", item.Type)
	}
}

func interactionEvent(name string, payload map[string]any) ([]responseStreamEvent, error) {
	payload["event_type"] = name
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return []responseStreamEvent{{Name: name, Payload: string(encoded)}}, nil
}
