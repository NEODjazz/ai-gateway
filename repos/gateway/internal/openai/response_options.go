package openai

import (
	"bytes"
	"encoding/json"
	"math"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ValidateEnvelope checks the required model and input shape shared by public
// Responses entry points before policy, accounting, and provider execution.
func (r ResponseRequest) ValidateEnvelope() string {
	if strings.TrimSpace(r.Model) == "" || utf8.RuneCountInString(r.Model) > 256 {
		return "model must contain between 1 and 256 characters"
	}
	if r.Input == nil {
		return "input is required"
	}
	if _, ok := r.Input.(string); ok {
		return ""
	}
	encoded, err := json.Marshal(r.Input)
	if err != nil {
		return "input must be a string or non-empty array of objects"
	}
	var items []json.RawMessage
	if json.Unmarshal(encoded, &items) != nil || len(items) == 0 {
		return "input must be a string or non-empty array of objects"
	}
	for _, item := range items {
		var object map[string]json.RawMessage
		if bytes.Equal(bytes.TrimSpace(item), []byte("null")) || json.Unmarshal(item, &object) != nil || object == nil {
			return "input array entries must be objects"
		}
	}
	return ""
}

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
	if message := validateResponseTools(r.Tools); message != "" {
		return message
	}
	if message := validateResponseToolChoice(r.Tools, r.ToolChoice); message != "" {
		return message
	}
	if message := validateResponseReasoning(r.Reasoning); message != "" {
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
	if message := validateResponseText(r.Text); message != "" {
		return message
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
	if r.MaxToolCalls != nil && (*r.MaxToolCalls < 0 || *r.MaxToolCalls > 1000) {
		return "max_tool_calls must be between 0 and 1000"
	}
	return ""
}

func validateResponseText(value any) string {
	if value == nil {
		return ""
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "text must be an object"
	}
	var config map[string]json.RawMessage
	if json.Unmarshal(encoded, &config) != nil || config == nil {
		return "text must be an object"
	}
	for key := range config {
		if key != "format" && key != "verbosity" {
			return "text contains an unsupported field"
		}
	}
	if raw, supplied := config["format"]; supplied && string(raw) != "null" {
		var format map[string]json.RawMessage
		if json.Unmarshal(raw, &format) != nil || format == nil {
			return "text.format must be an object"
		}
	}
	if raw, supplied := config["verbosity"]; supplied && string(raw) != "null" {
		var verbosity string
		if json.Unmarshal(raw, &verbosity) != nil || !validVerbosity(verbosity) {
			return "text.verbosity must be low, medium, or high"
		}
	}
	return ""
}

func validateResponseReasoning(reasoning *ResponseReasoning) string {
	if reasoning == nil {
		return ""
	}
	if reasoning.Effort != nil {
		switch *reasoning.Effort {
		case "none", "minimal", "low", "medium", "high", "xhigh", "max", "default":
		default:
			return "reasoning.effort contains an unsupported value"
		}
	}
	for _, summary := range []*string{reasoning.Summary, reasoning.GenerateSummary} {
		if summary == nil {
			continue
		}
		switch *summary {
		case "auto", "concise", "detailed":
		default:
			return "reasoning summary must be auto, concise, or detailed"
		}
	}
	if reasoning.Context != nil {
		switch *reasoning.Context {
		case "auto", "current_turn", "all_turns":
		default:
			return "reasoning.context must be auto, current_turn, or all_turns"
		}
	}
	if reasoning.Mode != nil && (strings.TrimSpace(*reasoning.Mode) == "" || utf8.RuneCountInString(*reasoning.Mode) > 128) {
		return "reasoning.mode must contain between 1 and 128 characters"
	}
	return ""
}

func validateResponseTools(tools []ResponseTool) string {
	if len(tools) > 128 {
		return "tools must contain at most 128 entries"
	}
	functionNames := make(map[string]struct{}, len(tools))
	mcpLabels := make(map[string]struct{}, len(tools))
	hostedTypes := make(map[string]struct{}, 2)
	for index, tool := range tools {
		switch tool.Type {
		case "function":
			if !chatFunctionName.MatchString(tool.Name) {
				return "function tool names must contain 1 to 64 letters, digits, underscores, or hyphens"
			}
			if utf8.RuneCountInString(tool.Description) > 4096 {
				return "function tool descriptions must contain at most 4096 characters"
			}
			if tool.ServerLabel != "" || tool.ServerURL != "" || tool.ServerDescription != "" || len(tool.AllowedTools) > 0 || tool.RequireApproval != nil || len(tool.Headers) > 0 || len(tool.VectorStoreIDs) > 0 || tool.Container != nil {
				return "function tools contain unsupported fields"
			}
			if tool.Parameters != nil && !isJSONObject(tool.Parameters) {
				return "function tool parameters must be an object"
			}
			if _, duplicate := functionNames[tool.Name]; duplicate {
				return "function tool names must be unique"
			}
			functionNames[tool.Name] = struct{}{}
		case "mcp":
			if strings.TrimSpace(tool.ServerLabel) == "" || !validResponseMCPURL(tool.ServerURL) {
				return "mcp tools require a server_label and safe HTTPS server_url"
			}
			if tool.Name != "" || tool.Description != "" || tool.Parameters != nil || tool.Strict != nil || len(tool.VectorStoreIDs) > 0 || tool.Container != nil {
				return "mcp tools contain unsupported fields"
			}
			if _, duplicate := mcpLabels[tool.ServerLabel]; duplicate {
				return "mcp server labels must be unique"
			}
			mcpLabels[tool.ServerLabel] = struct{}{}
			if len(tool.AllowedTools) > 128 {
				return "mcp allowed_tools must contain at most 128 names"
			}
			allowed := make(map[string]struct{}, len(tool.AllowedTools))
			for _, name := range tool.AllowedTools {
				if strings.TrimSpace(name) == "" {
					return "mcp allowed_tools must contain non-empty names"
				}
				if _, duplicate := allowed[name]; duplicate {
					return "mcp allowed_tools names must be unique"
				}
				allowed[name] = struct{}{}
			}
		case "code_interpreter":
			if _, duplicate := hostedTypes[tool.Type]; duplicate {
				return "code_interpreter tools must be unique"
			}
			hostedTypes[tool.Type] = struct{}{}
			if tool.Name != "" || tool.Description != "" || tool.Parameters != nil || tool.Strict != nil || tool.ServerLabel != "" || tool.ServerURL != "" || tool.ServerDescription != "" || len(tool.AllowedTools) > 0 || tool.RequireApproval != nil || len(tool.Headers) > 0 || len(tool.VectorStoreIDs) > 0 {
				return "code_interpreter tools contain unsupported fields"
			}
			if _, message := ResponseCodeInterpreterContainerFileIDs(tool.Container); message != "" {
				return message
			}
		case "file_search":
			if _, duplicate := hostedTypes[tool.Type]; duplicate {
				return "file_search tools must be unique"
			}
			hostedTypes[tool.Type] = struct{}{}
			if tool.Name != "" || tool.Description != "" || tool.Parameters != nil || tool.Strict != nil || tool.ServerLabel != "" || tool.ServerURL != "" || tool.ServerDescription != "" || len(tool.AllowedTools) > 0 || tool.RequireApproval != nil || len(tool.Headers) > 0 || tool.Container != nil {
				return "file_search tools contain unsupported fields"
			}
			if len(tool.VectorStoreIDs) == 0 || len(tool.VectorStoreIDs) > 1 {
				return "file_search tools require exactly one vector_store_id"
			}
			vectorStores := make(map[string]struct{}, len(tool.VectorStoreIDs))
			for _, id := range tool.VectorStoreIDs {
				if !validResponseToolResourceID(id) {
					return "file_search vector_store_ids contain an invalid ID"
				}
				if _, duplicate := vectorStores[id]; duplicate {
					return "file_search vector_store_ids must be unique"
				}
				vectorStores[id] = struct{}{}
			}
		default:
			return "tools contain an unsupported type at index " + strconv.Itoa(index)
		}
	}
	return ""
}

func isJSONObject(value any) bool {
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(encoded, &object) == nil && object != nil
}

// ResponseCodeInterpreterContainerFileIDs validates the supported automatic
// container shape and returns the referenced file IDs.
func ResponseCodeInterpreterContainerFileIDs(value any) ([]string, string) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, "code_interpreter tools require an object container"
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(encoded, &object) != nil || object == nil {
		return nil, "code_interpreter tools require an object container"
	}
	for field := range object {
		if field != "type" && field != "memory_limit" && field != "file_ids" {
			return nil, "code_interpreter container contains unsupported fields"
		}
	}
	var containerType string
	if json.Unmarshal(object["type"], &containerType) != nil || containerType != "auto" {
		return nil, "code_interpreter container.type must be auto"
	}
	if raw, present := object["memory_limit"]; present {
		var memoryLimit string
		if json.Unmarshal(raw, &memoryLimit) != nil || memoryLimit != "1g" && memoryLimit != "4g" && memoryLimit != "16g" && memoryLimit != "64g" {
			return nil, "code_interpreter container.memory_limit must be 1g, 4g, 16g, or 64g"
		}
	}
	var fileIDs []string
	if raw, present := object["file_ids"]; present {
		if json.Unmarshal(raw, &fileIDs) != nil {
			return nil, "code_interpreter container.file_ids must be an array"
		}
		if len(fileIDs) > 20 {
			return nil, "code_interpreter container.file_ids must contain at most 20 IDs"
		}
		seen := make(map[string]struct{}, len(fileIDs))
		for _, id := range fileIDs {
			if !validResponseToolResourceID(id) {
				return nil, "code_interpreter container.file_ids contain an invalid ID"
			}
			if _, duplicate := seen[id]; duplicate {
				return nil, "code_interpreter container.file_ids must be unique"
			}
			seen[id] = struct{}{}
		}
	}
	return fileIDs, ""
}

func validResponseToolResourceID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func validResponseMCPURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
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
	if err := json.Unmarshal(encoded, &object); err != nil || len(object) == 0 {
		return "tool_choice must be a supported string or object"
	}
	var kind, name, serverLabel string
	if err := json.Unmarshal(object["type"], &kind); err != nil {
		return "tool_choice must be a supported string or object"
	}
	switch kind {
	case "code_interpreter", "file_search":
		if len(object) == 1 {
			for _, tool := range tools {
				if tool.Type == kind {
					return ""
				}
			}
		}
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
