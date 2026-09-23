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
	if len(r.ContextManagement) > 1 {
		return "context_management must contain at most one entry"
	}
	if r.ContextManagement != nil && len(r.ContextManagement) == 0 {
		return "context_management must contain one entry when supplied"
	}
	for _, entry := range r.ContextManagement {
		if entry.Type != "compaction" {
			return "context_management type must be compaction"
		}
		if entry.CompactThreshold != nil && *entry.CompactThreshold <= 0 {
			return "context_management compact_threshold must be positive"
		}
	}
	if message := validateProviderModeration(r.Moderation); message != "" {
		return message
	}
	if message := validateResponseIncludes(r.Include); message != "" {
		return message
	}
	if message := validateResponseTools(r.Tools); message != "" {
		return message
	}
	if _, message := InspectResponseComputerCallOutputs(r.Input); message != "" {
		return message
	}
	if _, message := InspectResponseShellCallOutputs(r.Input); message != "" {
		return message
	}
	if _, message := InspectResponseApplyPatchCallOutputs(r.Input); message != "" {
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

func validateProviderModeration(moderation *ProviderModeration) string {
	if moderation == nil {
		return ""
	}
	if strings.TrimSpace(moderation.Model) == "" || utf8.RuneCountInString(moderation.Model) > 256 {
		return "moderation.model must contain between 1 and 256 characters"
	}
	if moderation.Policy == nil {
		return ""
	}
	for _, rule := range []*ProviderModerationRule{moderation.Policy.Input, moderation.Policy.Output} {
		if rule != nil && rule.Mode != "score" && rule.Mode != "block" {
			return "moderation policy mode must be score or block"
		}
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
	namedToolNames := make(map[string]struct{}, len(tools))
	mcpLabels := make(map[string]struct{}, len(tools))
	hostedTypes := make(map[string]struct{}, 4)
	for index, tool := range tools {
		switch tool.Type {
		case "function":
			if !chatFunctionName.MatchString(tool.Name) {
				return "function tool names must contain 1 to 64 letters, digits, underscores, or hyphens"
			}
			if utf8.RuneCountInString(tool.Description) > 4096 {
				return "function tool descriptions must contain at most 4096 characters"
			}
			if responseToolHasHostedImageFields(tool) || tool.ServerLabel != "" || tool.ServerURL != "" || tool.ServerDescription != "" || len(tool.AllowedTools) > 0 || len(tool.AllowedCallers) > 0 || tool.RequireApproval != nil || len(tool.Headers) > 0 || len(tool.VectorStoreIDs) > 0 || tool.Container != nil || tool.Environment != nil || tool.Filters != nil || tool.MaxNumResults != nil || tool.RankingOptions != nil || tool.RewriteQuery != nil || tool.SearchContextSize != "" || tool.UserLocation != nil || tool.Format != nil {
				return "function tools contain unsupported fields"
			}
			if tool.Parameters != nil && !isJSONObject(tool.Parameters) {
				return "function tool parameters must be an object"
			}
			if _, duplicate := namedToolNames[tool.Name]; duplicate {
				return "function and custom tool names must be unique"
			}
			namedToolNames[tool.Name] = struct{}{}
		case "custom":
			if !chatFunctionName.MatchString(tool.Name) {
				return "custom tool names must contain 1 to 64 letters, digits, underscores, or hyphens"
			}
			if utf8.RuneCountInString(tool.Description) > 4096 {
				return "custom tool descriptions must contain at most 4096 characters"
			}
			if responseToolHasHostedImageFields(tool) || tool.Parameters != nil || tool.Strict != nil || tool.ServerLabel != "" || tool.ServerURL != "" || tool.ServerDescription != "" || len(tool.AllowedTools) > 0 || len(tool.AllowedCallers) > 0 || tool.RequireApproval != nil || len(tool.Headers) > 0 || len(tool.VectorStoreIDs) > 0 || tool.Container != nil || tool.Environment != nil || tool.Filters != nil || tool.MaxNumResults != nil || tool.RankingOptions != nil || tool.RewriteQuery != nil || tool.SearchContextSize != "" || tool.UserLocation != nil {
				return "custom tools contain unsupported fields"
			}
			if message := validateResponseCustomToolFormat(tool.Format); message != "" {
				return message
			}
			if _, duplicate := namedToolNames[tool.Name]; duplicate {
				return "function and custom tool names must be unique"
			}
			namedToolNames[tool.Name] = struct{}{}
		case "mcp":
			if strings.TrimSpace(tool.ServerLabel) == "" || !validResponseMCPURL(tool.ServerURL) {
				return "mcp tools require a server_label and safe HTTPS server_url"
			}
			if responseToolHasHostedImageFields(tool) || tool.Name != "" || tool.Description != "" || tool.Parameters != nil || tool.Strict != nil || len(tool.AllowedCallers) > 0 || len(tool.VectorStoreIDs) > 0 || tool.Container != nil || tool.Environment != nil || tool.Filters != nil || tool.MaxNumResults != nil || tool.RankingOptions != nil || tool.RewriteQuery != nil || tool.SearchContextSize != "" || tool.UserLocation != nil || tool.Format != nil {
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
			if responseToolHasHostedImageFields(tool) || tool.Name != "" || tool.Description != "" || tool.Parameters != nil || tool.Strict != nil || tool.ServerLabel != "" || tool.ServerURL != "" || tool.ServerDescription != "" || len(tool.AllowedTools) > 0 || len(tool.AllowedCallers) > 0 || tool.RequireApproval != nil || len(tool.Headers) > 0 || len(tool.VectorStoreIDs) > 0 || tool.Environment != nil || tool.Filters != nil || tool.MaxNumResults != nil || tool.RankingOptions != nil || tool.RewriteQuery != nil || tool.SearchContextSize != "" || tool.UserLocation != nil || tool.Format != nil {
				return "code_interpreter tools contain unsupported fields"
			}
			if _, _, message := InspectResponseCodeInterpreterContainer(tool.Container); message != "" {
				return message
			}
		case "file_search":
			if _, duplicate := hostedTypes[tool.Type]; duplicate {
				return "file_search tools must be unique"
			}
			hostedTypes[tool.Type] = struct{}{}
			if responseToolHasHostedImageFields(tool) || tool.Name != "" || tool.Description != "" || tool.Parameters != nil || tool.Strict != nil || tool.ServerLabel != "" || tool.ServerURL != "" || tool.ServerDescription != "" || len(tool.AllowedTools) > 0 || len(tool.AllowedCallers) > 0 || tool.RequireApproval != nil || len(tool.Headers) > 0 || tool.Container != nil || tool.Environment != nil || tool.SearchContextSize != "" || tool.UserLocation != nil || tool.Format != nil {
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
			if message := validateResponseFileSearchOptions(tool); message != "" {
				return message
			}
		case "web_search", "web_search_2025_08_26", "web_search_preview", "web_search_preview_2025_03_11":
			if _, duplicate := hostedTypes["web_search"]; duplicate {
				return "web_search tools must be unique"
			}
			hostedTypes["web_search"] = struct{}{}
			if responseToolHasHostedImageFields(tool) || tool.Name != "" || tool.Description != "" || tool.Parameters != nil || tool.Strict != nil || tool.ServerLabel != "" || tool.ServerURL != "" || tool.ServerDescription != "" || len(tool.AllowedTools) > 0 || len(tool.AllowedCallers) > 0 || tool.RequireApproval != nil || len(tool.Headers) > 0 || len(tool.VectorStoreIDs) > 0 || tool.Container != nil || tool.Environment != nil || tool.MaxNumResults != nil || tool.RankingOptions != nil || tool.RewriteQuery != nil || tool.Format != nil {
				return "web_search tools contain unsupported fields"
			}
			if message := validateResponseWebSearchOptions(tool); message != "" {
				return message
			}
		case "image_generation":
			if _, duplicate := hostedTypes[tool.Type]; duplicate {
				return "image_generation tools must be unique"
			}
			hostedTypes[tool.Type] = struct{}{}
			if tool.Name != "" || tool.Description != "" || tool.Parameters != nil || tool.Strict != nil || tool.ServerLabel != "" || tool.ServerURL != "" || tool.ServerDescription != "" || len(tool.AllowedTools) > 0 || len(tool.AllowedCallers) > 0 || tool.RequireApproval != nil || len(tool.Headers) > 0 || len(tool.VectorStoreIDs) > 0 || tool.Container != nil || tool.Environment != nil || tool.Filters != nil || tool.MaxNumResults != nil || tool.RankingOptions != nil || tool.RewriteQuery != nil || tool.SearchContextSize != "" || tool.UserLocation != nil || tool.Format != nil {
				return "image_generation tools contain unsupported fields"
			}
			if message := validateResponseImageGenerationOptions(tool); message != "" {
				return message
			}
		case "computer":
			if _, duplicate := hostedTypes[tool.Type]; duplicate {
				return "computer tools must be unique"
			}
			hostedTypes[tool.Type] = struct{}{}
			if responseToolHasHostedImageFields(tool) || tool.Name != "" || tool.Description != "" || tool.Parameters != nil || tool.Strict != nil || tool.ServerLabel != "" || tool.ServerURL != "" || tool.ServerDescription != "" || len(tool.AllowedTools) > 0 || len(tool.AllowedCallers) > 0 || tool.RequireApproval != nil || len(tool.Headers) > 0 || len(tool.VectorStoreIDs) > 0 || tool.Container != nil || tool.Environment != nil || tool.Filters != nil || tool.MaxNumResults != nil || tool.RankingOptions != nil || tool.RewriteQuery != nil || tool.SearchContextSize != "" || tool.UserLocation != nil || tool.Format != nil {
				return "computer tools contain unsupported fields"
			}
		case "shell":
			if _, duplicate := hostedTypes[tool.Type]; duplicate {
				return "shell tools must be unique"
			}
			hostedTypes[tool.Type] = struct{}{}
			if responseToolHasHostedImageFields(tool) || tool.Name != "" || tool.Description != "" || tool.Parameters != nil || tool.Strict != nil || tool.ServerLabel != "" || tool.ServerURL != "" || tool.ServerDescription != "" || len(tool.AllowedTools) > 0 || tool.RequireApproval != nil || len(tool.Headers) > 0 || len(tool.VectorStoreIDs) > 0 || tool.Container != nil || tool.Filters != nil || tool.MaxNumResults != nil || tool.RankingOptions != nil || tool.RewriteQuery != nil || tool.SearchContextSize != "" || tool.UserLocation != nil || tool.Format != nil {
				return "shell tools contain unsupported fields"
			}
			if message := validateResponseShellAllowedCallers(tool.AllowedCallers); message != "" {
				return message
			}
			if _, _, _, message := InspectResponseShellEnvironment(tool.Environment); message != "" {
				return message
			}
		case "apply_patch":
			if _, duplicate := hostedTypes[tool.Type]; duplicate {
				return "apply_patch tools must be unique"
			}
			hostedTypes[tool.Type] = struct{}{}
			if responseToolHasHostedImageFields(tool) || tool.Name != "" || tool.Description != "" || tool.Parameters != nil || tool.Strict != nil || tool.ServerLabel != "" || tool.ServerURL != "" || tool.ServerDescription != "" || len(tool.AllowedTools) > 0 || tool.RequireApproval != nil || len(tool.Headers) > 0 || len(tool.VectorStoreIDs) > 0 || tool.Container != nil || tool.Environment != nil || tool.Filters != nil || tool.MaxNumResults != nil || tool.RankingOptions != nil || tool.RewriteQuery != nil || tool.SearchContextSize != "" || tool.UserLocation != nil || tool.Format != nil {
				return "apply_patch tools contain unsupported fields"
			}
			if message := validateResponseApplyPatchAllowedCallers(tool.AllowedCallers); message != "" {
				return message
			}
		default:
			return "tools contain an unsupported type at index " + strconv.Itoa(index)
		}
	}
	return ""
}

func responseToolHasHostedImageFields(tool ResponseTool) bool {
	return tool.Action != "" || tool.Background != "" || tool.InputFidelity != "" || tool.InputImageMask != nil || tool.Model != "" || tool.Moderation != "" || tool.OutputCompression != nil || tool.OutputFormat != "" || tool.PartialImages != nil || tool.Quality != "" || tool.Size != ""
}

func validateResponseImageGenerationOptions(tool ResponseTool) string {
	if !oneOfOrEmpty(tool.Action, "generate", "edit", "auto") {
		return "image_generation action must be generate, edit, or auto"
	}
	if !oneOfOrEmpty(tool.Background, "transparent", "opaque", "auto") {
		return "image_generation background must be transparent, opaque, or auto"
	}
	if !oneOfOrEmpty(tool.InputFidelity, "high", "low") {
		return "image_generation input_fidelity must be high or low"
	}
	if mask := tool.InputImageMask; mask != nil {
		if (mask.FileID == "") == (mask.ImageURL == "") {
			return "image_generation input_image_mask requires exactly one of file_id or image_url"
		}
		if mask.FileID != "" && !validResponseToolResourceID(mask.FileID) {
			return "image_generation input_image_mask.file_id is invalid"
		}
		if mask.ImageURL != "" {
			if len(mask.ImageURL) > 32<<20 {
				return "image_generation input_image_mask.image_url is too large"
			}
			if _, err := ParseDataImageURL(mask.ImageURL); err != nil {
				return "image_generation input_image_mask.image_url must be a valid base64 data image URL"
			}
		}
	}
	if strings.TrimSpace(tool.Model) != tool.Model || utf8.RuneCountInString(tool.Model) > 256 {
		return "image_generation model is invalid"
	}
	if !oneOfOrEmpty(tool.Moderation, "auto", "low") {
		return "image_generation moderation must be auto or low"
	}
	if tool.OutputCompression != nil && (*tool.OutputCompression < 0 || *tool.OutputCompression > 100) {
		return "image_generation output_compression must be between 0 and 100"
	}
	if !oneOfOrEmpty(tool.OutputFormat, "png", "webp", "jpeg") {
		return "image_generation output_format must be png, webp, or jpeg"
	}
	if tool.PartialImages != nil && (*tool.PartialImages < 0 || *tool.PartialImages > 3) {
		return "image_generation partial_images must be between 0 and 3"
	}
	if !oneOfOrEmpty(tool.Quality, "low", "medium", "high", "auto") {
		return "image_generation quality must be low, medium, high, or auto"
	}
	if !validResponseImageGenerationSize(tool.Size) {
		return "image_generation size must be auto or valid WIDTHxHEIGHT dimensions"
	}
	if tool.Background == "transparent" && tool.OutputFormat != "" && tool.OutputFormat != "png" && tool.OutputFormat != "webp" {
		return "image_generation transparent background requires png or webp output_format"
	}
	return ""
}

func validResponseImageGenerationSize(value string) bool {
	if value == "" || value == "auto" {
		return true
	}
	parts := strings.Split(value, "x")
	if len(parts) != 2 {
		return false
	}
	width, widthErr := strconv.Atoi(parts[0])
	height, heightErr := strconv.Atoi(parts[1])
	return widthErr == nil && heightErr == nil && width >= 256 && height >= 256 && width <= 3840 && height <= 3840 && width%16 == 0 && height%16 == 0 && width <= height*3 && height <= width*3
}

func validateResponseCustomToolFormat(format *ResponseCustomToolFormat) string {
	if format == nil {
		return ""
	}
	switch format.Type {
	case "text":
		if format.Syntax != "" || format.Definition != "" {
			return "custom text format accepts only type"
		}
	case "grammar":
		if format.Syntax != "lark" && format.Syntax != "regex" {
			return "custom grammar syntax must be lark or regex"
		}
		if format.Definition == "" || !utf8.ValidString(format.Definition) || len(format.Definition) > 64<<10 {
			return "custom grammar definition must contain between 1 and 65536 UTF-8 bytes"
		}
	default:
		return "custom tool format type must be text or grammar"
	}
	return ""
}

// IsResponseWebSearchTool reports whether a Responses tool type is a supported
// current or preview web-search contract.
func IsResponseWebSearchTool(toolType string) bool {
	switch toolType {
	case "web_search", "web_search_2025_08_26", "web_search_preview", "web_search_preview_2025_03_11":
		return true
	default:
		return false
	}
}

func validateResponseWebSearchOptions(tool ResponseTool) string {
	switch tool.SearchContextSize {
	case "", "low", "medium", "high":
	default:
		return "web_search search_context_size must be low, medium, or high"
	}
	if tool.Filters != nil {
		if tool.Type == "web_search_preview" || tool.Type == "web_search_preview_2025_03_11" {
			return "web_search preview tools do not support filters"
		}
		encoded, err := json.Marshal(tool.Filters)
		if err != nil || len(encoded) > 16<<10 {
			return "web_search filters must be a bounded object"
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(encoded, &object) != nil || object == nil || !onlyResponseFilterKeys(object, "allowed_domains") {
			return "web_search filters accept only allowed_domains"
		}
		if raw, supplied := object["allowed_domains"]; supplied {
			var domains []string
			if json.Unmarshal(raw, &domains) != nil || !validNativeSearchDomains(domains) {
				return "web_search allowed_domains contain an invalid or duplicate value"
			}
		}
	}
	if location := tool.UserLocation; location != nil {
		if location.Type != "approximate" {
			return "web_search user_location requires type=approximate"
		}
		for _, value := range []string{location.City, location.Region, location.Timezone} {
			if strings.TrimSpace(value) != value || utf8.RuneCountInString(value) > 128 {
				return "web_search user_location contains an invalid value"
			}
		}
		if location.Country != "" && (len(location.Country) != 2 || location.Country[0] < 'A' || location.Country[0] > 'Z' || location.Country[1] < 'A' || location.Country[1] > 'Z') {
			return "web_search user_location.country must be a two-letter uppercase country code"
		}
	}
	return ""
}

func validateResponseFileSearchOptions(tool ResponseTool) string {
	if tool.Filters != nil {
		if message := validateResponseFileSearchFilter(tool.Filters); message != "" {
			return message
		}
	}
	if tool.MaxNumResults != nil && (*tool.MaxNumResults < 1 || *tool.MaxNumResults > 50) {
		return "file_search max_num_results must be between 1 and 50"
	}
	if options := tool.RankingOptions; options != nil {
		if options.Ranker != "" && (strings.TrimSpace(options.Ranker) != options.Ranker || utf8.RuneCountInString(options.Ranker) > 128) {
			return "file_search ranking_options.ranker is invalid"
		}
		if options.ScoreThreshold != nil && (!finiteNumber(*options.ScoreThreshold) || *options.ScoreThreshold < 0 || *options.ScoreThreshold > 1) {
			return "file_search ranking_options.score_threshold must be between 0 and 1"
		}
		if hybrid := options.HybridSearch; hybrid != nil {
			if hybrid.EmbeddingWeight == nil && hybrid.TextWeight == nil {
				return "file_search hybrid_search requires at least one weight"
			}
			for _, weight := range []*float64{hybrid.EmbeddingWeight, hybrid.TextWeight} {
				if weight != nil && (!finiteNumber(*weight) || *weight < 0 || *weight > 1) {
					return "file_search hybrid search weights must be between 0 and 1"
				}
			}
		}
	}
	return ""
}

func validateResponseFileSearchFilter(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > 64<<10 {
		return "file_search filters must be a bounded object"
	}
	nodes := 0
	return validateResponseFileSearchFilterNode(encoded, 1, &nodes)
}

func validateResponseFileSearchFilterNode(raw json.RawMessage, depth int, nodes *int) string {
	*nodes++
	if depth > 4 || *nodes > 32 {
		return "file_search filters exceed the maximum depth or node count"
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return "file_search filters must contain objects"
	}
	var kind string
	if json.Unmarshal(object["type"], &kind) != nil {
		return "file_search filter type is required"
	}
	if kind == "and" || kind == "or" {
		if !onlyResponseFilterKeys(object, "type", "filters") {
			return "file_search compound filters accept only type and filters"
		}
		var children []json.RawMessage
		if json.Unmarshal(object["filters"], &children) != nil || len(children) < 1 || len(children) > 16 {
			return "file_search compound filters require 1 to 16 children"
		}
		for _, child := range children {
			if message := validateResponseFileSearchFilterNode(child, depth+1, nodes); message != "" {
				return message
			}
		}
		return ""
	}
	if !onlyResponseFilterKeys(object, "type", "key", "value") {
		return "file_search comparison filters accept only type, key and value"
	}
	allowed := map[string]bool{"eq": true, "ne": true, "gt": true, "gte": true, "lt": true, "lte": true, "in": true, "nin": true}
	if !allowed[kind] {
		return "file_search filter type is unsupported"
	}
	var key string
	if json.Unmarshal(object["key"], &key) != nil || key == "" || !utf8.ValidString(key) || utf8.RuneCountInString(key) > 64 {
		return "file_search filter key must contain 1 to 64 valid UTF-8 characters"
	}
	decoder := json.NewDecoder(bytes.NewReader(object["value"]))
	decoder.UseNumber()
	var valueAny any
	if decoder.Decode(&valueAny) != nil {
		return "file_search filter value is required"
	}
	values := []any{valueAny}
	if kind == "in" || kind == "nin" {
		var ok bool
		values, ok = valueAny.([]any)
		if !ok || len(values) < 1 || len(values) > 16 {
			return "file_search membership filters require 1 to 16 values"
		}
	}
	for _, item := range values {
		switch typed := item.(type) {
		case string:
			if !utf8.ValidString(typed) || utf8.RuneCountInString(typed) > 512 {
				return "file_search filter string values must contain at most 512 valid UTF-8 characters"
			}
			if kind == "gt" || kind == "gte" || kind == "lt" || kind == "lte" {
				return "file_search ordered filters require a numeric value"
			}
		case bool:
			if kind == "gt" || kind == "gte" || kind == "lt" || kind == "lte" {
				return "file_search ordered filters require a numeric value"
			}
		case json.Number:
			number, err := typed.Float64()
			if err != nil || !finiteNumber(number) {
				return "file_search filter numeric values must be finite"
			}
		default:
			return "file_search filter values must be strings, booleans or numbers"
		}
	}
	return ""
}

func onlyResponseFilterKeys(object map[string]json.RawMessage, allowed ...string) bool {
	if len(object) != len(allowed) {
		return false
	}
	for _, key := range allowed {
		if _, present := object[key]; !present {
			return false
		}
	}
	return true
}

func isJSONObject(value any) bool {
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(encoded, &object) == nil && object != nil
}

// InspectResponseCodeInterpreterContainer validates a reusable container ID or
// an automatic container definition and returns its resource references.
func InspectResponseCodeInterpreterContainer(value any) (string, []string, string) {
	if id, ok := value.(string); ok {
		if !validResponseToolResourceID(id) {
			return "", nil, "code_interpreter container ID is invalid"
		}
		return id, nil, ""
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", nil, "code_interpreter tools require a container ID or object"
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(encoded, &object) != nil || object == nil {
		return "", nil, "code_interpreter tools require a container ID or object"
	}
	for field := range object {
		if field != "type" && field != "memory_limit" && field != "file_ids" {
			return "", nil, "code_interpreter container contains unsupported fields"
		}
	}
	var containerType string
	if json.Unmarshal(object["type"], &containerType) != nil || containerType != "auto" {
		return "", nil, "code_interpreter container.type must be auto"
	}
	if raw, present := object["memory_limit"]; present {
		var memoryLimit string
		if json.Unmarshal(raw, &memoryLimit) != nil || memoryLimit != "1g" && memoryLimit != "4g" && memoryLimit != "16g" && memoryLimit != "64g" {
			return "", nil, "code_interpreter container.memory_limit must be 1g, 4g, 16g, or 64g"
		}
	}
	var fileIDs []string
	if raw, present := object["file_ids"]; present {
		if json.Unmarshal(raw, &fileIDs) != nil {
			return "", nil, "code_interpreter container.file_ids must be an array"
		}
		if len(fileIDs) > 20 {
			return "", nil, "code_interpreter container.file_ids must contain at most 20 IDs"
		}
		seen := make(map[string]struct{}, len(fileIDs))
		for _, id := range fileIDs {
			if !validResponseToolResourceID(id) {
				return "", nil, "code_interpreter container.file_ids contain an invalid ID"
			}
			if _, duplicate := seen[id]; duplicate {
				return "", nil, "code_interpreter container.file_ids must be unique"
			}
			seen[id] = struct{}{}
		}
	}
	return "", fileIDs, ""
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
	case "code_interpreter", "file_search", "image_generation", "computer", "shell", "apply_patch":
		if len(object) == 1 {
			for _, tool := range tools {
				if tool.Type == kind {
					return ""
				}
			}
		}
	case "web_search", "web_search_2025_08_26", "web_search_preview", "web_search_preview_2025_03_11":
		if len(object) == 1 {
			for _, tool := range tools {
				if IsResponseWebSearchTool(tool.Type) {
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
	case "custom":
		if len(object) != 2 || json.Unmarshal(object["name"], &name) != nil || name == "" {
			return "tool_choice must reference an available tool"
		}
		for _, tool := range tools {
			if tool.Type == "custom" && tool.Name == name {
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
