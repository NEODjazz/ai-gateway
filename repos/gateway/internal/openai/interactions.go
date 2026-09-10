package openai

import (
	"encoding/json"
	"strings"
	"time"
)

type InteractionRequest struct {
	Provider              string                      `json:"provider,omitempty"`
	Model                 string                      `json:"model,omitempty"`
	Agent                 string                      `json:"agent,omitempty"`
	Input                 any                         `json:"input"`
	SystemInstruction     string                      `json:"system_instruction,omitempty"`
	Tools                 []ResponseTool              `json:"tools,omitempty"`
	ResponseFormat        any                         `json:"response_format,omitempty"`
	PreviousInteractionID string                      `json:"previous_interaction_id,omitempty"`
	Store                 *bool                       `json:"store,omitempty"`
	Stream                bool                        `json:"stream,omitempty"`
	Background            bool                        `json:"background,omitempty"`
	GenerationConfig      InteractionGenerationConfig `json:"generation_config,omitempty"`
}

type InteractionGenerationConfig struct {
	MaxOutputTokens *int     `json:"max_output_tokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"top_p,omitempty"`
	Seed            *int64   `json:"seed,omitempty"`
	StopSequences   []string `json:"stop_sequences,omitempty"`
	ThinkingLevel   string   `json:"thinking_level,omitempty"`
}

func (r InteractionRequest) ResponseRequest() (ResponseRequest, string) {
	if strings.TrimSpace(r.Agent) != "" {
		return ResponseRequest{}, "agent interactions are not supported"
	}
	if strings.TrimSpace(r.Model) == "" {
		return ResponseRequest{}, "model is required"
	}
	switch input := r.Input.(type) {
	case string:
		if strings.TrimSpace(input) == "" {
			return ResponseRequest{}, "input is required"
		}
	case []any:
		if len(input) == 0 {
			return ResponseRequest{}, "input is required"
		}
	case nil:
		return ResponseRequest{}, "input is required"
	default:
		return ResponseRequest{}, "input must be a string or a non-empty array"
	}
	if r.Stream {
		return ResponseRequest{}, "streaming interactions are not supported"
	}
	if r.Background {
		return ResponseRequest{}, "background interactions are not supported"
	}
	if r.GenerationConfig.Seed != nil {
		return ResponseRequest{}, "generation_config.seed is not supported"
	}
	if len(r.GenerationConfig.StopSequences) > 0 {
		return ResponseRequest{}, "generation_config.stop_sequences is not supported"
	}
	if r.GenerationConfig.ThinkingLevel != "" {
		return ResponseRequest{}, "generation_config.thinking_level is not supported"
	}
	for _, tool := range r.Tools {
		if tool.Type != "function" {
			return ResponseRequest{}, "only function tools are supported for interactions"
		}
	}
	if r.GenerationConfig.Temperature != nil && (*r.GenerationConfig.Temperature < 0 || *r.GenerationConfig.Temperature > 2) {
		return ResponseRequest{}, "generation_config.temperature must be between 0 and 2"
	}
	if r.GenerationConfig.TopP != nil && (*r.GenerationConfig.TopP < 0 || *r.GenerationConfig.TopP > 1) {
		return ResponseRequest{}, "generation_config.top_p must be between 0 and 1"
	}
	var text any
	if r.ResponseFormat != nil {
		text = map[string]any{"format": r.ResponseFormat}
	}
	result := ResponseRequest{
		Provider: r.Provider, Model: r.Model, Input: r.Input, Instructions: r.SystemInstruction,
		Tools: r.Tools, Text: text, PreviousResponse: r.PreviousInteractionID, Store: r.Store,
		MaxOutputTokens: r.GenerationConfig.MaxOutputTokens, Temperature: r.GenerationConfig.Temperature,
		TopP: r.GenerationConfig.TopP,
	}
	if message := result.Validate(); message != "" {
		return ResponseRequest{}, message
	}
	return result, ""
}

type InteractionResponse struct {
	ID                string                     `json:"id"`
	Object            string                     `json:"object"`
	Created           string                     `json:"created,omitempty"`
	Updated           string                     `json:"updated,omitempty"`
	Model             string                     `json:"model"`
	Status            string                     `json:"status"`
	Steps             []InteractionStep          `json:"steps,omitempty"`
	Usage             InteractionUsage           `json:"usage,omitempty"`
	Error             *ResponseError             `json:"error,omitempty"`
	IncompleteDetails *ResponseIncompleteDetails `json:"incomplete_details,omitempty"`
}

type InteractionStep struct {
	ID        string                       `json:"id,omitempty"`
	Type      string                       `json:"type"`
	Name      string                       `json:"name,omitempty"`
	Arguments any                          `json:"arguments,omitempty"`
	Content   []InteractionResponseContent `json:"content,omitempty"`
}

type InteractionResponseContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type InteractionUsage struct {
	TotalCachedTokens  int `json:"total_cached_tokens,omitempty"`
	TotalInputTokens   int `json:"total_input_tokens,omitempty"`
	TotalOutputTokens  int `json:"total_output_tokens,omitempty"`
	TotalThoughtTokens int `json:"total_thought_tokens,omitempty"`
	TotalTokens        int `json:"total_tokens,omitempty"`
}

func InteractionFromResponse(response ResponseResponse) InteractionResponse {
	result := InteractionResponse{
		ID: response.ID, Object: "interaction", Model: response.Model, Status: response.Status,
		Error: response.Error, IncompleteDetails: response.IncompleteDetails,
	}
	if response.CreatedAt > 0 {
		result.Created = time.Unix(response.CreatedAt, 0).UTC().Format(time.RFC3339)
		result.Updated = result.Created
	}
	for _, item := range response.Output {
		switch item.Type {
		case "message":
			step := InteractionStep{ID: item.ID, Type: "model_output"}
			for _, content := range item.Content {
				text := content.Text
				if text == "" {
					text = content.Refusal
				}
				if text != "" {
					step.Content = append(step.Content, InteractionResponseContent{Type: "text", Text: text})
				}
			}
			result.Steps = append(result.Steps, step)
		case "function_call":
			arguments := any(item.Arguments)
			var decoded any
			if json.Unmarshal([]byte(item.Arguments), &decoded) == nil {
				arguments = decoded
			}
			id := item.CallID
			if id == "" {
				id = item.ID
			}
			result.Steps = append(result.Steps, InteractionStep{ID: id, Type: "function_call", Name: item.Name, Arguments: arguments})
		case "reasoning":
			step := InteractionStep{ID: item.ID, Type: "thought"}
			for _, content := range item.Summary {
				if content.Text != "" {
					step.Content = append(step.Content, InteractionResponseContent{Type: "text", Text: content.Text})
				}
			}
			result.Steps = append(result.Steps, step)
		}
	}
	result.Usage = InteractionUsage{
		TotalInputTokens: response.Usage.InputTokens, TotalOutputTokens: response.Usage.OutputTokens,
		TotalTokens: response.Usage.TotalTokens,
	}
	if response.Usage.InputTokensDetails != nil {
		result.Usage.TotalCachedTokens = response.Usage.InputTokensDetails.CachedTokens
	}
	if response.Usage.OutputTokensDetails != nil {
		result.Usage.TotalThoughtTokens = response.Usage.OutputTokensDetails.ReasoningTokens
	}
	return result
}
