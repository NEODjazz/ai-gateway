package openai

import (
	"encoding/json"
	"math"
	"unicode/utf8"
)

// Validate checks provider-independent Responses generation options.
func (r ResponseRequest) Validate() string {
	if r.Background && r.Stream {
		return "background and stream cannot both be enabled"
	}
	if r.StreamOptions != nil && !r.Stream {
		return "stream_options requires stream=true"
	}
	if r.Background && (r.Store == nil || !*r.Store) {
		return "background requires store=true"
	}
	if message := ValidateMetadata(r.Metadata); message != "" {
		return message
	}
	if message := validateResponseIncludes(r.Include); message != "" {
		return message
	}
	if message := validateResponseToolChoice(r.Tools, r.ToolChoice); message != "" {
		return message
	}
	if utf8.RuneCountInString(r.SafetyIdentifier) > 64 {
		return "safety_identifier must contain at most 64 characters"
	}
	if message := ValidatePromptCacheOptions(r.PromptCacheOptions); message != "" {
		return message
	}
	if r.PromptCacheRetention != "" && r.PromptCacheRetention != "in_memory" && r.PromptCacheRetention != "24h" {
		return "prompt_cache_retention must be in_memory or 24h"
	}
	if !validServiceTier(r.ServiceTier) {
		return "unsupported service_tier value"
	}
	if _, _, valid := responseTextVerbosity(r.Text); !valid {
		return "text.verbosity must be low, medium, or high"
	}
	if r.MaxOutputTokens != nil && r.MaxTokens != nil {
		return "max_output_tokens and max_tokens are mutually exclusive"
	}
	if r.MaxOutputTokens != nil && *r.MaxOutputTokens <= 0 {
		return "max_output_tokens must be positive"
	}
	if r.MaxTokens != nil && *r.MaxTokens <= 0 {
		return "max_tokens must be positive"
	}
	if r.TopLogprobs != nil && (*r.TopLogprobs < 0 || *r.TopLogprobs > 20) {
		return "top_logprobs must be between 0 and 20"
	}
	if r.Truncation != nil && *r.Truncation != "auto" && *r.Truncation != "disabled" {
		return "truncation must be auto or disabled"
	}
	if r.Temperature != nil && (math.IsNaN(*r.Temperature) || math.IsInf(*r.Temperature, 0) || *r.Temperature < 0 || *r.Temperature > 2) {
		return "temperature must be between 0 and 2"
	}
	if r.TopP != nil && (math.IsNaN(*r.TopP) || math.IsInf(*r.TopP, 0) || *r.TopP < 0 || *r.TopP > 1) {
		return "top_p must be between 0 and 1"
	}
	for _, penalty := range []*float64{r.FrequencyPenalty, r.PresencePenalty} {
		if penalty != nil && (math.IsNaN(*penalty) || math.IsInf(*penalty, 0) || *penalty < -2 || *penalty > 2) {
			return "frequency_penalty and presence_penalty must be between -2 and 2"
		}
	}
	if r.MaxToolCalls != nil && (*r.MaxToolCalls < 1 || *r.MaxToolCalls > 1000) {
		return "max_tool_calls must be between 1 and 1000"
	}
	return ""
}

func validateResponseToolChoice(tools []ResponseTool, choice any) string {
	if choice == nil {
		return ""
	}
	if value, ok := choice.(string); ok {
		switch value {
		case "none":
			return ""
		case "auto", "required":
			if len(tools) > 0 {
				return ""
			}
		}
		return "tool_choice must reference an available tool"
	}
	encoded, err := json.Marshal(choice)
	if err != nil {
		return "tool_choice must be a supported string or object"
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil || len(object) < 2 {
		return "tool_choice must be a supported string or object"
	}
	var kind, name, serverLabel string
	if err := json.Unmarshal(object["type"], &kind); err != nil {
		return "tool_choice must be a supported string or object"
	}
	switch kind {
	case "function":
		if len(object) != 2 || json.Unmarshal(object["name"], &name) != nil || name == "" {
			return "tool_choice must reference an available tool"
		}
		for _, tool := range tools {
			if tool.Type == "function" && tool.Name == name {
				return ""
			}
		}
	case "mcp":
		if len(object) != 3 || json.Unmarshal(object["server_label"], &serverLabel) != nil || json.Unmarshal(object["name"], &name) != nil || serverLabel == "" || name == "" {
			return "tool_choice must reference an available tool"
		}
		for _, tool := range tools {
			if tool.Type != "mcp" || tool.ServerLabel != serverLabel {
				continue
			}
			if len(tool.AllowedTools) == 0 {
				return ""
			}
			for _, allowed := range tool.AllowedTools {
				if allowed == name {
					return ""
				}
			}
		}
	}
	return "tool_choice must reference an available tool"
}

func validateResponseIncludes(include []string) string {
	if len(include) > 7 {
		return "include must contain at most 7 values"
	}
	seen := make(map[string]struct{}, len(include))
	for _, value := range include {
		switch value {
		case "web_search_call.action.sources",
			"code_interpreter_call.outputs",
			"computer_call_output.output.image_url",
			"file_search_call.results",
			"message.input_image.image_url",
			"message.output_text.logprobs",
			"reasoning.encrypted_content":
		default:
			return "include contains an unsupported value"
		}
		if _, duplicate := seen[value]; duplicate {
			return "include values must be unique"
		}
		seen[value] = struct{}{}
	}
	return ""
}

// ValidateMetadata checks the shared metadata limits used by inference contracts.
func ValidateMetadata(metadata map[string]string) string {
	if len(metadata) > 16 {
		return "metadata must contain at most 16 entries"
	}
	for key, value := range metadata {
		if utf8.RuneCountInString(key) > 64 || utf8.RuneCountInString(value) > 512 {
			return "metadata keys must be at most 64 characters and values at most 512 characters"
		}
	}
	return ""
}
