package openai

import (
	"encoding/json"
	"math"
	"mime"
	"strings"
	"time"
)

type InteractionRequest struct {
	Provider              string                      `json:"provider,omitempty"`
	Model                 string                      `json:"model,omitempty"`
	Agent                 string                      `json:"agent,omitempty"`
	Environment           string                      `json:"environment,omitempty"`
	Input                 any                         `json:"input"`
	SystemInstruction     string                      `json:"system_instruction,omitempty"`
	Tools                 []ResponseTool              `json:"tools,omitempty"`
	ResponseFormat        any                         `json:"response_format,omitempty"`
	ResponseMIMEType      string                      `json:"response_mime_type,omitempty"`
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
		return ResponseRequest{}, "agent interactions require a native agent-capable deployment"
	}
	result, message := r.NativeResponseRequest()
	if message != "" {
		return ResponseRequest{}, message
	}
	if strings.TrimSpace(r.Model) == "" {
		return ResponseRequest{}, "model is required"
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
	return result, ""
}

// NativeResponseRequest validates the model or agent Interactions contract and
// returns its shared Responses representation for policy, token, and billing modules.
func (r InteractionRequest) NativeResponseRequest() (ResponseRequest, string) {
	model, agent := strings.TrimSpace(r.Model), strings.TrimSpace(r.Agent)
	if model == "" && agent == "" {
		return ResponseRequest{}, "model or agent is required"
	}
	if model != "" && agent != "" {
		return ResponseRequest{}, "model and agent are mutually exclusive"
	}
	environment := strings.TrimSpace(r.Environment)
	if environment != "" {
		if agent == "" {
			return ResponseRequest{}, "environment requires an agent interaction"
		}
		if strings.TrimSpace(r.PreviousInteractionID) == "" {
			return ResponseRequest{}, "environment reuse requires previous_interaction_id"
		}
		if !ValidInteractionResourceID(environment) || strings.EqualFold(environment, "remote") {
			return ResponseRequest{}, "environment must reference an existing environment ID"
		}
	}
	effectiveModel := model
	if effectiveModel == "" {
		effectiveModel = agent
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
	if r.GenerationConfig.Seed != nil && (*r.GenerationConfig.Seed < math.MinInt32 || *r.GenerationConfig.Seed > math.MaxInt32) {
		return ResponseRequest{}, "generation_config.seed must be a signed 32-bit integer"
	}
	if len(r.GenerationConfig.StopSequences) > 5 {
		return ResponseRequest{}, "generation_config.stop_sequences must contain at most 5 strings"
	}
	for _, sequence := range r.GenerationConfig.StopSequences {
		if strings.TrimSpace(sequence) == "" {
			return ResponseRequest{}, "generation_config.stop_sequences must contain non-empty strings"
		}
	}
	if level := r.GenerationConfig.ThinkingLevel; level != "" && level != "minimal" && level != "low" && level != "medium" && level != "high" {
		return ResponseRequest{}, "generation_config.thinking_level is invalid"
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
	responseFormat, message := normalizedInteractionResponseFormat(r.ResponseFormat, r.ResponseMIMEType)
	if message != "" {
		return ResponseRequest{}, message
	}
	var text any
	if responseFormat != nil {
		text = map[string]any{"format": responseFormat}
	}
	store := r.Store
	if r.Background && store == nil {
		stored := true
		store = &stored
	}
	result := ResponseRequest{
		Provider: r.Provider, Model: effectiveModel, Input: r.Input, Instructions: r.SystemInstruction,
		Tools: r.Tools, Text: text, PreviousResponse: r.PreviousInteractionID, Store: store, Stream: r.Stream, Background: r.Background,
		MaxOutputTokens: r.GenerationConfig.MaxOutputTokens, Temperature: r.GenerationConfig.Temperature,
		TopP: r.GenerationConfig.TopP,
	}
	if environment != "" {
		result.NativeInputTokens = EstimateContextTokens(environment)
	}
	if message := result.Validate(); message != "" {
		return ResponseRequest{}, message
	}
	return result, ""
}

func (r InteractionRequest) WithResponseRequest(shared ResponseRequest) InteractionRequest {
	r.Provider, r.Input, r.SystemInstruction = shared.Provider, shared.Input, shared.Instructions
	if strings.TrimSpace(r.Agent) != "" {
		r.Agent, r.Model = shared.Model, ""
	} else {
		r.Model = shared.Model
	}
	r.Tools, r.ResponseFormat, r.PreviousInteractionID = shared.Tools, nil, shared.PreviousResponse
	if text, ok := shared.Text.(map[string]any); ok {
		r.ResponseFormat = text["format"]
	}
	r.ResponseMIMEType = ""
	r.Store, r.Stream, r.Background = shared.Store, shared.Stream, shared.Background
	r.GenerationConfig.MaxOutputTokens, r.GenerationConfig.Temperature, r.GenerationConfig.TopP = shared.MaxOutputTokens, shared.Temperature, shared.TopP
	return r
}

func normalizedInteractionResponseFormat(format any, rawMIMEType string) (any, string) {
	rawMIMEType = strings.TrimSpace(rawMIMEType)
	if rawMIMEType == "" {
		return format, ""
	}
	if len(rawMIMEType) > 128 {
		return nil, "response_mime_type is too long"
	}
	mediaType, _, err := mime.ParseMediaType(rawMIMEType)
	if err != nil || !(strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")) {
		return nil, "response_mime_type must be text or JSON"
	}
	if _, ok := format.([]any); ok {
		return nil, "response_mime_type cannot be combined with an array response_format"
	}
	if entry, ok := format.(map[string]any); ok {
		if existing, found := entry["mime_type"]; found {
			value, valid := existing.(string)
			if !valid || !strings.EqualFold(strings.TrimSpace(value), mediaType) {
				return nil, "response_mime_type conflicts with response_format.mime_type"
			}
			return format, ""
		}
	}
	result := map[string]any{"type": "text", "mime_type": mediaType}
	if format != nil {
		result["schema"] = format
	}
	return result, ""
}

type InteractionResponse struct {
	ID                string                     `json:"id"`
	Object            string                     `json:"object"`
	Created           string                     `json:"created,omitempty"`
	Updated           string                     `json:"updated,omitempty"`
	Model             string                     `json:"model,omitempty"`
	Agent             string                     `json:"agent,omitempty"`
	EnvironmentID     string                     `json:"environment_id,omitempty"`
	Status            string                     `json:"status"`
	Steps             []InteractionStep          `json:"steps,omitempty"`
	Usage             InteractionUsage           `json:"usage,omitempty"`
	Error             *ResponseError             `json:"error,omitempty"`
	IncompleteDetails *ResponseIncompleteDetails `json:"incomplete_details,omitempty"`
}

func ValidInteractionResourceID(value string) bool {
	if value = strings.TrimSpace(value); value == "" || len(value) > 256 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' && char != '-' && char != '.' && char != ':' {
			return false
		}
	}
	return true
}

func ResponseFromInteraction(interaction InteractionResponse) ResponseResponse {
	response := ResponseResponse{ID: interaction.ID, Object: "response", Model: interaction.Model, Status: interaction.Status, Error: interaction.Error, IncompleteDetails: interaction.IncompleteDetails}
	if created, err := time.Parse(time.RFC3339Nano, interaction.Created); err == nil {
		response.CreatedAt = created.Unix()
	}
	for _, step := range interaction.Steps {
		switch step.Type {
		case "model_output":
			item := ResponseOutputItem{ID: step.ID, Type: "message", Role: "assistant"}
			for _, content := range step.Content {
				if content.Text != "" {
					item.Content = append(item.Content, ResponseOutputContent{Type: "output_text", Text: content.Text})
				}
			}
			response.Output = append(response.Output, item)
		case "function_call":
			arguments, _ := json.Marshal(step.Arguments)
			response.Output = append(response.Output, ResponseOutputItem{ID: step.ID, CallID: step.ID, Type: "function_call", Name: step.Name, Arguments: string(arguments)})
		case "thought":
			item := ResponseOutputItem{ID: step.ID, Type: "reasoning"}
			for _, content := range step.Content {
				if content.Text != "" {
					item.Summary = append(item.Summary, ResponseOutputContent{Type: "summary_text", Text: content.Text})
				}
			}
			response.Output = append(response.Output, item)
		}
	}
	response.Usage = ResponseUsage{InputTokens: interaction.Usage.TotalInputTokens, OutputTokens: interaction.Usage.TotalOutputTokens, TotalTokens: interaction.Usage.TotalTokens}
	if interaction.Usage.TotalCachedTokens > 0 {
		response.Usage.InputTokensDetails = &InputTokenDetails{CachedTokens: interaction.Usage.TotalCachedTokens}
	}
	if interaction.Usage.TotalThoughtTokens > 0 {
		response.Usage.OutputTokensDetails = &CompletionTokenDetails{ReasoningTokens: interaction.Usage.TotalThoughtTokens}
	}
	return response
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
