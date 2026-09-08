package openai

import "encoding/json"

// DefaultOutputTokenReserve is used when a generation request has no output cap.
const DefaultOutputTokenReserve = 1024

// ChatOutputLimit normalizes the two mutually exclusive wire parameters.
func ChatOutputLimit(r ChatCompletionRequest) int {
	if r.MaxCompletionTokens != nil {
		return max(0, *r.MaxCompletionTokens)
	}
	if r.MaxTokens != nil {
		return max(0, *r.MaxTokens)
	}
	return 0
}

func ResponseOutputLimit(r ResponseRequest) int {
	if r.MaxOutputTokens != nil {
		return max(0, *r.MaxOutputTokens)
	}
	if r.MaxTokens != nil {
		return max(0, *r.MaxTokens)
	}
	return 0
}

// EstimateContextTokens estimates the serialized input, including roles, tool
// schemas and tool arguments. It is not a provider tokenizer. Encoded image
// bytes are replaced with a bounded image allowance rather than counted as text.
func EstimateContextTokens(value any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 1
	}
	var input any
	if json.Unmarshal(encoded, &input) != nil {
		return 1
	}
	images := 0
	var scrub func(any) any
	scrub = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			if x["type"] == "image_url" || x["type"] == "input_image" {
				images++
				return "image"
			}
			for k, child := range x {
				x[k] = scrub(child)
			}
		case []any:
			for i, child := range x {
				x[i] = scrub(child)
			}
		}
		return v
	}
	encoded, _ = json.Marshal(scrub(input))
	return max(1, (len(encoded)+3)/4+images*4096)
}

func ChatInputTokens(r ChatCompletionRequest) int {
	return EstimateContextTokens(struct {
		Messages   []Message       `json:"messages"`
		Tools      []Tool          `json:"tools,omitempty"`
		ToolChoice any             `json:"tool_choice,omitempty"`
		Format     *ResponseFormat `json:"response_format,omitempty"`
	}{r.Messages, r.Tools, r.ToolChoice, r.ResponseFormat})
}

func ResponseInputTokens(r ResponseRequest) int {
	return EstimateContextTokens(struct {
		Input        any            `json:"input"`
		Instructions string         `json:"instructions,omitempty"`
		Tools        []ResponseTool `json:"tools,omitempty"`
		ToolChoice   any            `json:"tool_choice,omitempty"`
		Text         any            `json:"text,omitempty"`
	}{r.Input, r.Instructions, r.Tools, r.ToolChoice, r.Text})
}

// ReserveTokens saturates instead of overflowing for an untrusted output cap.
func ReserveTokens(input, output int) int {
	if output <= 0 {
		output = DefaultOutputTokenReserve
	}
	limit := int(^uint(0) >> 1)
	if input > limit-output {
		return limit
	}
	return input + output
}
