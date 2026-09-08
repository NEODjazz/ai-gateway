package gateway

import (
	"encoding/json"
	"fmt"

	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

// synthesizeResponseStream replays a completed JSON result after provider modules
// and billing finish. It does not execute a second generation.
func synthesizeResponseStream(response openai.ResponseResponse, write provider.ResponseStreamWriter) error {
	if response.Status == "" {
		response.Status = "completed"
	}
	switch response.Status {
	case "completed", "incomplete", "failed":
	default:
		return fmt.Errorf("cannot synthesize a stream from a non-terminal response")
	}
	response.Output = append([]openai.ResponseOutputItem(nil), response.Output...)
	if len(response.Output) == 0 && response.OutputText != "" {
		response.Output = []openai.ResponseOutputItem{{Type: "message", Role: "assistant", Status: response.Status, Content: []openai.ResponseOutputContent{{Type: "output_text", Text: response.OutputText}}}}
	}
	for i := range response.Output {
		if response.Output[i].ID == "" {
			response.Output[i].ID = fmt.Sprintf("%s_item_%d", response.ID, i)
		}
	}
	sequence := 0
	emit := func(kind string, data map[string]any) error {
		data["type"], data["sequence_number"] = kind, sequence
		payload, err := json.Marshal(data)
		if err != nil {
			return err
		}
		sequence++
		return write(kind, string(payload))
	}
	initial := response
	initial.Error, initial.IncompleteDetails = nil, nil
	initial.Status, initial.Output, initial.OutputText, initial.Usage = "in_progress", nil, "", openai.ResponseUsage{}
	if err := emit("response.created", map[string]any{"response": struct {
		openai.ResponseResponse
		Output []openai.ResponseOutputItem `json:"output"`
	}{initial, []openai.ResponseOutputItem{}}}); err != nil {
		return err
	}
	for index, item := range response.Output {
		added := map[string]any{"id": item.ID, "type": item.Type, "status": "in_progress"}
		if item.Role != "" {
			added["role"] = item.Role
		}
		if item.Name != "" {
			added["name"] = item.Name
		}
		if item.CallID != "" {
			added["call_id"] = item.CallID
		}
		if item.Type == "message" {
			added["content"] = []any{}
		}
		if item.Type == "function_call" {
			added["arguments"] = ""
		}
		if item.Type == "reasoning" {
			added["summary"] = []any{}
		}
		if err := emit("response.output_item.added", map[string]any{"output_index": index, "item": added}); err != nil {
			return err
		}
		for contentIndex, part := range item.Content {
			empty := part
			empty.Text = ""
			empty.Refusal = ""
			fields := func() map[string]any {
				return map[string]any{"item_id": item.ID, "output_index": index, "content_index": contentIndex}
			}
			data := fields()
			data["part"] = syntheticResponsePart(empty)
			if err := emit("response.content_part.added", data); err != nil {
				return err
			}
			if part.Type == "output_text" {
				data = fields()
				data["delta"] = part.Text
				if err := emit("response.output_text.delta", data); err != nil {
					return err
				}
				data = fields()
				data["text"] = part.Text
				if err := emit("response.output_text.done", data); err != nil {
					return err
				}
			}
			if part.Type == "refusal" {
				data = fields()
				data["delta"] = part.Refusal
				if err := emit("response.refusal.delta", data); err != nil {
					return err
				}
				data = fields()
				data["refusal"] = part.Refusal
				if err := emit("response.refusal.done", data); err != nil {
					return err
				}
			}
			data = fields()
			data["part"] = syntheticResponsePart(part)
			if err := emit("response.content_part.done", data); err != nil {
				return err
			}
		}
		if item.Type == "reasoning" {
			for summaryIndex, part := range item.Summary {
				for _, kind := range []string{"response.reasoning_summary_part.added", "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done", "response.reasoning_summary_part.done"} {
					data := map[string]any{"item_id": item.ID, "output_index": index, "summary_index": summaryIndex}
					switch kind {
					case "response.reasoning_summary_part.added":
						data["part"] = map[string]any{"type": "summary_text", "text": ""}
					case "response.reasoning_summary_text.delta":
						data["delta"] = part.Text
					case "response.reasoning_summary_text.done":
						data["text"] = part.Text
					case "response.reasoning_summary_part.done":
						data["part"] = map[string]any{"type": "summary_text", "text": part.Text}
					}
					if err := emit(kind, data); err != nil {
						return err
					}
				}
			}
		}
		if item.Type == "function_call" {
			for _, suffix := range []string{"delta", "done"} {
				data := map[string]any{"item_id": item.ID, "output_index": index}
				if suffix == "delta" {
					data["delta"] = item.Arguments
				} else {
					data["arguments"] = item.Arguments
				}
				if err := emit("response.function_call_arguments."+suffix, data); err != nil {
					return err
				}
			}
		}
		if err := emit("response.output_item.done", map[string]any{"output_index": index, "item": item}); err != nil {
			return err
		}
	}
	return emit("response."+response.Status, map[string]any{"response": response})
}

func syntheticResponsePart(part openai.ResponseOutputContent) any {
	if part.Type == "refusal" {
		return map[string]any{"type": part.Type, "refusal": part.Refusal}
	}
	if part.Type == "output_text" {
		return map[string]any{"type": part.Type, "text": part.Text, "annotations": []any{}}
	}
	return part
}
