package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"ai-gateway-gateway/internal/openai"
)

type messagesRequest struct {
	Model         string                `json:"model"`
	MaxTokens     int                   `json:"max_tokens"`
	Messages      []messagesInput       `json:"messages"`
	System        json.RawMessage       `json:"system,omitempty"`
	Tools         []messagesTool        `json:"tools,omitempty"`
	ToolChoice    *messagesToolChoice   `json:"tool_choice,omitempty"`
	Metadata      *messagesMetadata     `json:"metadata,omitempty"`
	OutputConfig  *messagesOutputConfig `json:"output_config,omitempty"`
	ServiceTier   string                `json:"service_tier,omitempty"`
	Temperature   *float64              `json:"temperature,omitempty"`
	TopP          *float64              `json:"top_p,omitempty"`
	Stream        bool                  `json:"stream,omitempty"`
	StopSequences []string              `json:"stop_sequences,omitempty"`
	Container     *messagesContainer    `json:"container,omitempty"`
}
type messagesContainer struct {
	ID     string                           `json:"id,omitempty"`
	Skills []openai.AnthropicSkillReference `json:"skills,omitempty"`
}
type messagesMetadata struct {
	UserID string `json:"user_id,omitempty"`
}
type messagesOutputConfig struct {
	Effort string                    `json:"effort,omitempty"`
	Format *messagesJSONOutputFormat `json:"format,omitempty"`
}
type messagesJSONOutputFormat struct {
	Type   string         `json:"type"`
	Schema map[string]any `json:"schema"`
}
type messagesInput struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}
type messagesTool struct {
	Type              string                `json:"type,omitempty"`
	Name              string                `json:"name"`
	Description       string                `json:"description,omitempty"`
	InputSchema       map[string]any        `json:"input_schema,omitempty"`
	CacheControl      *messagesCacheControl `json:"cache_control,omitempty"`
	MaxUses           *int                  `json:"max_uses,omitempty"`
	UserLocation      *messagesUserLocation `json:"user_location,omitempty"`
	AllowedDomains    []string              `json:"allowed_domains,omitempty"`
	BlockedDomains    []string              `json:"blocked_domains,omitempty"`
	AllowedCallers    []string              `json:"allowed_callers,omitempty"`
	ResponseInclusion string                `json:"response_inclusion,omitempty"`
	UseCache          *bool                 `json:"use_cache,omitempty"`
	MaxContentTokens  int                   `json:"max_content_tokens,omitempty"`
	Citations         *messagesCitations    `json:"citations,omitempty"`
	DeferLoading      bool                  `json:"defer_loading,omitempty"`
}

type messagesUserLocation struct {
	Type        string                                   `json:"type"`
	City        string                                   `json:"city,omitempty"`
	Country     string                                   `json:"country,omitempty"`
	Region      string                                   `json:"region,omitempty"`
	Timezone    string                                   `json:"timezone,omitempty"`
	Approximate *openai.ChatWebSearchApproximateLocation `json:"approximate,omitempty"`
}

func (location *messagesUserLocation) chat() (*openai.ChatWebSearchUserLocation, bool) {
	if location == nil {
		return nil, true
	}
	flat := openai.ChatWebSearchApproximateLocation{City: location.City, Country: location.Country, Region: location.Region, Timezone: location.Timezone}
	if location.Type != "approximate" || (location.Approximate != nil && (flat.City != "" || flat.Country != "" || flat.Region != "" || flat.Timezone != "")) {
		return nil, false
	}
	if location.Approximate != nil {
		flat = *location.Approximate
	}
	if flat.City == "" && flat.Country == "" && flat.Region == "" && flat.Timezone == "" {
		return nil, false
	}
	return &openai.ChatWebSearchUserLocation{Type: "approximate", Approximate: &flat}, true
}

type messagesCitations struct {
	Enabled bool `json:"enabled"`
}
type messagesToolChoice struct {
	Type            string `json:"type"`
	Name            string `json:"name,omitempty"`
	DisableParallel *bool  `json:"disable_parallel_tool_use,omitempty"`
}
type messagesCacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

func decodeMessagesValue(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}

func (request messagesRequest) chat() (openai.ChatCompletionRequest, error) {
	return request.chatContext(false)
}

func (request messagesRequest) chatContext(allowPartial bool) (openai.ChatCompletionRequest, error) {
	result := openai.ChatCompletionRequest{Model: request.Model, MaxTokens: &request.MaxTokens, Temperature: request.Temperature, TopP: request.TopP, Stream: request.Stream}
	if request.Container != nil {
		if request.Stream {
			return result, errors.New("streaming with container.skills is not supported")
		}
		if request.Container.ID != "" && (!validSkillID(request.Container.ID) || len(request.Container.ID) > 128) {
			return result, errors.New("container.id is invalid")
		}
		if len(request.Container.Skills) == 0 || len(request.Container.Skills) > 20 {
			return result, errors.New("container.skills must contain 1–20 skills")
		}
		seen := make(map[string]struct{}, len(request.Container.Skills))
		for _, skill := range request.Container.Skills {
			if (skill.Type != "anthropic" && skill.Type != "custom") || !validSkillID(skill.SkillID) || len(skill.SkillID) > 64 || (skill.Version != "" && (!validSkillID(skill.Version) || len(skill.Version) > 64)) {
				return result, errors.New("container.skills contains an invalid skill reference")
			}
			key := skill.Type + "\x00" + skill.SkillID
			if _, duplicate := seen[key]; duplicate {
				return result, errors.New("container.skills contains a duplicate skill reference")
			}
			seen[key] = struct{}{}
		}
		result.AnthropicSkills = append([]openai.AnthropicSkillReference(nil), request.Container.Skills...)
		result.AnthropicContainerID = request.Container.ID
		result.NativeInputTokens = openai.ReserveTokens(result.NativeInputTokens, openai.EstimateContextTokens(request.Container))
	}
	switch request.ServiceTier {
	case "", "auto", "standard_only":
		result.ServiceTier = request.ServiceTier
	default:
		return result, errors.New("service_tier must be auto or standard_only")
	}
	if request.Metadata != nil {
		if utf8.RuneCountInString(request.Metadata.UserID) > 512 {
			return result, errors.New("metadata.user_id must contain at most 512 characters")
		}
		if request.Metadata.UserID != "" {
			result.Metadata = map[string]string{"user_id": request.Metadata.UserID}
		}
	}
	if config := request.OutputConfig; config != nil {
		switch config.Effort {
		case "", "low", "medium", "high", "xhigh", "max":
			result.ReasoningEffort = config.Effort
		default:
			return result, errors.New("output_config.effort must be low, medium, high, xhigh, or max")
		}
		if config.Format != nil {
			if config.Format.Type != "json_schema" || config.Format.Schema == nil {
				return result, errors.New("output_config.format requires type json_schema and schema")
			}
			strict := true
			result.ResponseFormat = &openai.ResponseFormat{Type: "json_schema", JSONSchema: &openai.JSONSchemaFormat{Name: "messages_output", Schema: config.Format.Schema, Strict: &strict}}
		}
	}
	if request.Stream {
		result.StreamOptions = &openai.ChatStreamOptions{IncludeUsage: true}
	}
	if len(request.StopSequences) > 0 {
		if _, valid := openai.StopSequences(request.StopSequences); !valid {
			return result, errors.New("stop_sequences must contain 1–4 non-empty strings")
		}
		result.Stop = request.StopSequences
		result.RequireMatchedStop = true
	}
	if strings.TrimSpace(request.Model) == "" || request.MaxTokens <= 0 || len(request.Messages) == 0 || len(request.Messages) > 10000 {
		return result, errors.New("model, positive max_tokens and 1–10000 messages are required")
	}
	if request.Temperature != nil && (*request.Temperature < 0 || *request.Temperature > 1) {
		return result, errors.New("temperature must be between 0 and 1")
	}
	if request.TopP != nil && (*request.TopP < 0 || *request.TopP > 1) {
		return result, errors.New("top_p must be between 0 and 1")
	}
	if len(request.System) > 0 {
		content, err := messagesSystem(request.System)
		if err != nil {
			return result, fmt.Errorf("system: %w", err)
		}
		result.Messages = append(result.Messages, openai.Message{Role: "system", Content: content})
	}
	knownCalls := map[string]bool{}
	for _, message := range request.Messages {
		if message.Role != "user" && message.Role != "assistant" {
			return result, errors.New("messages role must be user or assistant")
		}
		var text string
		if json.Unmarshal(message.Content, &text) == nil && !bytes.Equal(bytes.TrimSpace(message.Content), []byte("null")) {
			result.Messages = append(result.Messages, openai.Message{Role: message.Role, Content: text})
			continue
		}
		var blocks []json.RawMessage
		if err := json.Unmarshal(message.Content, &blocks); err != nil || len(blocks) == 0 {
			return result, errors.New("content must be text or a non-empty block array")
		}
		if native, content, err := messagesNativeAssistantContent(message.Role, blocks, knownCalls); err != nil {
			return result, err
		} else if native {
			result.Messages = append(result.Messages, openai.Message{Role: "assistant", NativeContent: content})
			result.NativeInputTokens = openai.ReserveTokens(result.NativeInputTokens, openai.EstimateContextTokens(content))
			continue
		}
		converted := openai.Message{Role: message.Role}
		parts := []any{}
		flush := func() {
			if len(parts) > 0 || len(converted.ToolCalls) > 0 || len(converted.Reasoning) > 0 {
				converted.Content = parts
				result.Messages = append(result.Messages, converted)
				converted = openai.Message{Role: message.Role}
				parts = []any{}
			}
		}
		for _, raw := range blocks {
			var kind struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(raw, &kind); err != nil {
				return result, errors.New("invalid content block")
			}
			switch kind.Type {
			case "thinking":
				var block struct {
					Type      string `json:"type"`
					Thinking  string `json:"thinking"`
					Signature string `json:"signature"`
				}
				if err := decodeMessagesValue(raw, &block); err != nil || message.Role != "assistant" || len(parts) > 0 || len(converted.ToolCalls) > 0 {
					return result, errors.New("invalid or misplaced thinking block")
				}
				converted.Reasoning = append(converted.Reasoning, openai.ReasoningBlock{Type: block.Type, Thinking: block.Thinking, Signature: block.Signature})
				if err := openai.ValidateReasoningBlocks(converted.Reasoning); err != nil {
					return result, err
				}
			case "redacted_thinking":
				var block struct {
					Type string `json:"type"`
					Data string `json:"data"`
				}
				if err := decodeMessagesValue(raw, &block); err != nil || message.Role != "assistant" || len(parts) > 0 || len(converted.ToolCalls) > 0 {
					return result, errors.New("invalid or misplaced redacted_thinking block")
				}
				converted.Reasoning = append(converted.Reasoning, openai.ReasoningBlock{Type: block.Type, Data: block.Data})
				if err := openai.ValidateReasoningBlocks(converted.Reasoning); err != nil {
					return result, err
				}
			case "text":
				if len(converted.ToolCalls) > 0 {
					return result, errors.New("text after tool_use is not supported")
				}
				var block struct {
					Type         string                `json:"type"`
					Text         string                `json:"text"`
					CacheControl *messagesCacheControl `json:"cache_control,omitempty"`
				}
				if err := decodeMessagesValue(raw, &block); err != nil {
					return result, fmt.Errorf("text block: %w", err)
				}
				part := map[string]any{"type": "text", "text": block.Text}
				if block.CacheControl != nil {
					breakpoint, err := messagesPromptCacheBreakpoint(block.CacheControl)
					if err != nil {
						return result, err
					}
					part["prompt_cache_breakpoint"] = map[string]any{"mode": breakpoint.Mode}
					if breakpoint.TTL != "" {
						part["prompt_cache_breakpoint"].(map[string]any)["ttl"] = breakpoint.TTL
					}
				}
				parts = append(parts, part)
			case "image":
				var block struct {
					Type   string `json:"type"`
					Source struct {
						Type      string `json:"type"`
						MediaType string `json:"media_type"`
						Data      string `json:"data"`
					} `json:"source"`
				}
				if err := decodeMessagesValue(raw, &block); err != nil || block.Source.Type != "base64" || message.Role != "user" {
					return result, errors.New("only base64 user image blocks are supported")
				}
				data := "data:" + block.Source.MediaType + ";base64," + block.Source.Data
				if _, err := openai.ParseDataImageURL(data); err != nil {
					return result, err
				}
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": data}})
			case "tool_use":
				var block struct {
					Type  string         `json:"type"`
					ID    string         `json:"id"`
					Name  string         `json:"name"`
					Input map[string]any `json:"input"`
				}
				if err := decodeMessagesValue(raw, &block); err != nil || message.Role != "assistant" || block.ID == "" || block.Name == "" || block.Input == nil || knownCalls[block.ID] {
					return result, errors.New("invalid or unsupported tool_use block")
				}
				knownCalls[block.ID] = true
				args, err := json.Marshal(block.Input)
				if err != nil {
					return result, err
				}
				converted.ToolCalls = append(converted.ToolCalls, openai.ToolCall{ID: block.ID, Type: "function", Function: openai.FunctionCall{Name: block.Name, Arguments: string(args)}})
			case "tool_result":
				var block struct {
					Type    string          `json:"type"`
					ID      string          `json:"tool_use_id"`
					Content json.RawMessage `json:"content"`
					IsError bool            `json:"is_error,omitempty"`
				}
				if err := decodeMessagesValue(raw, &block); err != nil || message.Role != "user" || !knownCalls[block.ID] || block.IsError {
					return result, errors.New("invalid or unsupported tool_result block")
				}
				content, err := messagesText(block.Content)
				if err != nil {
					return result, fmt.Errorf("tool_result: %w", err)
				}
				flush()
				result.Messages = append(result.Messages, openai.Message{Role: "tool", ToolCallID: block.ID, Content: content})
				delete(knownCalls, block.ID)
			default:
				return result, fmt.Errorf("unsupported content block type %q", kind.Type)
			}
		}
		flush()
	}
	if !allowPartial && request.Messages[len(request.Messages)-1].Role == "assistant" {
		last := &result.Messages[len(result.Messages)-1]
		if last.Role != "assistant" || strings.TrimSpace(openai.ContentText(last.Content)) == "" || len(last.ToolCalls) > 0 {
			return result, errors.New("assistant prefill requires non-empty text content")
		}
		prefix := true
		last.Prefix = &prefix
	}
	if !allowPartial && len(knownCalls) > 0 {
		return result, errors.New("all tool_use blocks require a tool_result")
	}
	if len(request.Tools) > 128 {
		return result, errors.New("too many tools")
	}
	searchTool, fetchTool, toolSearch := false, false, false
	for _, tool := range request.Tools {
		switch tool.Type {
		case "tool_search_tool_regex_20251119", "tool_search_tool_bm25_20251119":
			expectedName := strings.TrimSuffix(tool.Type, "_20251119")
			if toolSearch || tool.Name != expectedName || tool.InputSchema != nil || tool.Description != "" || tool.CacheControl != nil || tool.MaxUses != nil || tool.UserLocation != nil || messagesToolHasNativeWebFields(tool) || tool.MaxContentTokens != 0 || tool.Citations != nil || tool.DeferLoading {
				return result, errors.New("invalid or duplicate tool search tool")
			}
			toolSearch = true
			result.AnthropicToolSearch = tool.Type
			result.NativeInputTokens = openai.ReserveTokens(result.NativeInputTokens, openai.EstimateContextTokens(tool))
			continue
		case "code_execution_20250825", "code_execution_20260120", "code_execution_20260521":
			if result.AnthropicCodeExecution || tool.Name != "code_execution" || tool.InputSchema != nil || tool.Description != "" || tool.CacheControl != nil || tool.MaxUses != nil || tool.UserLocation != nil || messagesToolHasNativeWebFields(tool) || tool.MaxContentTokens != 0 || tool.Citations != nil || tool.DeferLoading {
				return result, errors.New("invalid or duplicate code execution tool")
			}
			result.AnthropicCodeExecution = true
			result.AnthropicCodeExecutionType = tool.Type
			result.NativeInputTokens = openai.ReserveTokens(result.NativeInputTokens, openai.EstimateContextTokens(tool))
			continue
		case "web_search_20250305", "web_search_20260209", "web_search_20260318":
			location, validLocation := tool.UserLocation.chat()
			if searchTool || tool.Name != "web_search" || tool.InputSchema != nil || tool.Description != "" || tool.CacheControl != nil || tool.MaxContentTokens != 0 || tool.Citations != nil || tool.UseCache != nil || tool.DeferLoading || !validLocation {
				return result, errors.New("invalid or duplicate web search tool")
			}
			if tool.ResponseInclusion != "" && tool.Type != "web_search_20260318" {
				return result, errors.New("web search response_inclusion requires web_search_20260318")
			}
			searchTool = true
			result.WebSearchOptions = &openai.ChatWebSearchOptions{MaxUses: tool.MaxUses, UserLocation: location, NativeType: tool.Type, AllowedDomains: append([]string(nil), tool.AllowedDomains...), BlockedDomains: append([]string(nil), tool.BlockedDomains...), AllowedCallers: append([]string(nil), tool.AllowedCallers...), ResponseInclusion: tool.ResponseInclusion}
			result.NativeInputTokens = openai.ReserveTokens(result.NativeInputTokens, openai.EstimateContextTokens(tool))
			continue
		case "web_fetch_20250910", "web_fetch_20260209", "web_fetch_20260309", "web_fetch_20260318":
			if fetchTool || tool.Name != "web_fetch" || tool.InputSchema != nil || tool.Description != "" || tool.CacheControl != nil || tool.UserLocation != nil || len(tool.BlockedDomains) > 0 || (tool.Citations != nil && !tool.Citations.Enabled) || tool.DeferLoading {
				return result, errors.New("invalid or duplicate web fetch tool")
			}
			if tool.UseCache != nil && tool.Type != "web_fetch_20260309" && tool.Type != "web_fetch_20260318" {
				return result, errors.New("web fetch use_cache requires web_fetch_20260309 or later")
			}
			if tool.ResponseInclusion != "" && tool.Type != "web_fetch_20260318" {
				return result, errors.New("web fetch response_inclusion requires web_fetch_20260318")
			}
			fetchTool = true
			result.WebFetchOptions = &openai.ChatWebFetchOptions{AllowedDomains: tool.AllowedDomains, MaxUses: tool.MaxUses, MaxContentTokens: tool.MaxContentTokens, NativeType: tool.Type, AllowedCallers: append([]string(nil), tool.AllowedCallers...), UseCache: tool.UseCache, ResponseInclusion: tool.ResponseInclusion}
			result.NativeInputTokens = openai.ReserveTokens(result.NativeInputTokens, openai.EstimateContextTokens(tool))
			continue
		case "":
		default:
			return result, errors.New("unsupported server tool")
		}
		if tool.Name == "" || tool.InputSchema == nil || tool.MaxUses != nil || tool.UserLocation != nil || messagesToolHasNativeWebFields(tool) || tool.MaxContentTokens != 0 || tool.Citations != nil {
			return result, errors.New("function tools require name and input_schema")
		}
		var breakpoint *openai.PromptCacheBreakpoint
		if tool.CacheControl != nil {
			if tool.DeferLoading {
				return result, errors.New("defer_loading cannot be combined with cache_control")
			}
			var err error
			breakpoint, err = messagesPromptCacheBreakpoint(tool.CacheControl)
			if err != nil {
				return result, err
			}
		}
		result.Tools = append(result.Tools, openai.Tool{Type: "function", Function: openai.FunctionDefinition{Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema, PromptCacheBreakpoint: breakpoint, DeferLoading: tool.DeferLoading}})
	}
	for _, tool := range result.Tools {
		if tool.Function.DeferLoading && !toolSearch {
			return result, errors.New("defer_loading requires a tool search tool")
		}
	}
	if len(result.AnthropicSkills) > 0 && !result.AnthropicCodeExecution {
		return result, errors.New("container.skills requires the code_execution_20250825 tool")
	}
	if message := result.ChatGenerationOptions.Validate(); message != "" {
		return result, errors.New(message)
	}
	if choice := request.ToolChoice; choice != nil {
		if choice.Name != "" && choice.Type != "tool" {
			return result, errors.New("tool_choice name requires type tool")
		}
		switch choice.Type {
		case "auto":
			result.ToolChoice = "auto"
		case "any":
			result.ToolChoice = "required"
		case "none":
			result.ToolChoice = "none"
		case "tool":
			if choice.Name == "" {
				return result, errors.New("tool_choice name required")
			}
			result.ToolChoice = map[string]any{"type": "function", "function": map[string]any{"name": choice.Name}}
		default:
			return result, errors.New("unsupported tool_choice")
		}
		if choice.DisableParallel != nil {
			enabled := !*choice.DisableParallel
			result.ParallelToolCalls = &enabled
		}
	}
	return result, nil
}

func messagesToolHasNativeWebFields(tool messagesTool) bool {
	return len(tool.AllowedDomains) > 0 || len(tool.BlockedDomains) > 0 || len(tool.AllowedCallers) > 0 || tool.ResponseInclusion != "" || tool.UseCache != nil
}

func messagesNativeAssistantContent(role string, blocks []json.RawMessage, knownCalls map[string]bool) (bool, []json.RawMessage, error) {
	native := false
	for _, raw := range blocks {
		var kind struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &kind) == nil && (kind.Type == "server_tool_use" || strings.HasSuffix(kind.Type, "_tool_result") && kind.Type != "tool_result") {
			native = true
		}
	}
	if !native {
		return false, nil, nil
	}
	if role != "assistant" || len(blocks) > 128 {
		return true, nil, errors.New("native server tool content requires a bounded assistant block array")
	}
	serverCalls := map[string]string{}
	result := make([]json.RawMessage, 0, len(blocks))
	total := 0
	for _, raw := range blocks {
		if !json.Valid(raw) || len(raw) > 4<<20 || total > (32<<20)-len(raw) {
			return true, nil, errors.New("native server tool content exceeds its size limit")
		}
		total += len(raw)
		var block map[string]json.RawMessage
		if err := json.Unmarshal(raw, &block); err != nil {
			return true, nil, errors.New("invalid native server tool content")
		}
		var kind string
		if err := json.Unmarshal(block["type"], &kind); err != nil {
			return true, nil, errors.New("invalid native server tool content type")
		}
		switch kind {
		case "text", "thinking", "redacted_thinking":
		case "tool_use":
			var id, name string
			var input map[string]any
			if json.Unmarshal(block["id"], &id) != nil || json.Unmarshal(block["name"], &name) != nil || json.Unmarshal(block["input"], &input) != nil || id == "" || name == "" || knownCalls[id] {
				return true, nil, errors.New("invalid native tool_use block")
			}
			knownCalls[id] = true
		case "server_tool_use":
			var id, name string
			var input map[string]any
			if json.Unmarshal(block["id"], &id) != nil || json.Unmarshal(block["name"], &name) != nil || json.Unmarshal(block["input"], &input) != nil || id == "" || name == "" || serverCalls[id] != "" {
				return true, nil, errors.New("invalid server_tool_use block")
			}
			serverCalls[id] = name
		case "tool_search_tool_result", "web_search_tool_result", "web_fetch_tool_result", "code_execution_tool_result", "bash_code_execution_tool_result", "text_editor_code_execution_tool_result":
			var id string
			if json.Unmarshal(block["tool_use_id"], &id) != nil || id == "" || serverCalls[id] == "" || block["content"] == nil {
				return true, nil, errors.New("invalid server tool result block")
			}
			delete(serverCalls, id)
		default:
			return true, nil, fmt.Errorf("unsupported native content block type %q", kind)
		}
		result = append(result, append(json.RawMessage(nil), raw...))
	}
	if len(serverCalls) > 0 {
		return true, nil, errors.New("server_tool_use requires a matching result block")
	}
	return true, result, nil
}

func messagesSystem(raw json.RawMessage) (any, error) {
	if text, err := messagesText(raw); err == nil {
		return text, nil
	}
	var blocks []struct {
		Type         string                `json:"type"`
		Text         string                `json:"text"`
		CacheControl *messagesCacheControl `json:"cache_control,omitempty"`
	}
	if err := decodeMessagesValue(raw, &blocks); err != nil || len(blocks) == 0 {
		return nil, errors.New("system must be text or non-empty text blocks")
	}
	parts := make([]any, 0, len(blocks))
	for _, block := range blocks {
		if block.Type != "text" {
			return nil, errors.New("system supports only text blocks")
		}
		part := map[string]any{"type": "text", "text": block.Text}
		if block.CacheControl != nil {
			breakpoint, err := messagesPromptCacheBreakpoint(block.CacheControl)
			if err != nil {
				return nil, err
			}
			value := map[string]any{"mode": breakpoint.Mode}
			if breakpoint.TTL != "" {
				value["ttl"] = breakpoint.TTL
			}
			part["prompt_cache_breakpoint"] = value
		}
		parts = append(parts, part)
	}
	return parts, nil
}

func messagesPromptCacheBreakpoint(value *messagesCacheControl) (*openai.PromptCacheBreakpoint, error) {
	if value == nil {
		return nil, nil
	}
	if value.Type != "ephemeral" || (value.TTL != "" && value.TTL != "5m" && value.TTL != "1h") {
		return nil, errors.New("cache_control requires type=ephemeral and optional ttl=5m or 1h")
	}
	return &openai.PromptCacheBreakpoint{Mode: "explicit", TTL: value.TTL}, nil
}

func messagesText(raw json.RawMessage) (string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return text, nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := decodeMessagesValue(raw, &blocks); err != nil || len(blocks) == 0 {
		return "", errors.New("expected text or text blocks")
	}
	var result strings.Builder
	for _, block := range blocks {
		if block.Type != "text" {
			return "", errors.New("only text blocks are supported here")
		}
		result.WriteString(block.Text)
	}
	return result.String(), nil
}
