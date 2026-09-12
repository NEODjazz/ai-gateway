package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"
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
	Type              string                                 `json:"type,omitempty"`
	Name              string                                 `json:"name"`
	Description       string                                 `json:"description,omitempty"`
	InputSchema       map[string]any                         `json:"input_schema,omitempty"`
	CacheControl      *messagesCacheControl                  `json:"cache_control,omitempty"`
	MaxUses           *int                                   `json:"max_uses,omitempty"`
	UserLocation      *messagesUserLocation                  `json:"user_location,omitempty"`
	AllowedDomains    []string                               `json:"allowed_domains,omitempty"`
	BlockedDomains    []string                               `json:"blocked_domains,omitempty"`
	AllowedCallers    []string                               `json:"allowed_callers,omitempty"`
	ResponseInclusion string                                 `json:"response_inclusion,omitempty"`
	UseCache          *bool                                  `json:"use_cache,omitempty"`
	MaxContentTokens  int                                    `json:"max_content_tokens,omitempty"`
	MaxCharacters     *int                                   `json:"max_characters,omitempty"`
	Citations         *messagesCitations                     `json:"citations,omitempty"`
	DeferLoading      bool                                   `json:"defer_loading,omitempty"`
	Configs           map[string]messagesToolsetMemberConfig `json:"configs,omitempty"`
}

type messagesToolsetMemberConfig struct {
	Enabled      *bool `json:"enabled,omitempty"`
	DeferLoading *bool `json:"defer_loading,omitempty"`
}

type messagesKnownToolCall struct {
	ToolsetName string
	ToolName    string
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
	knownCalls := map[string]messagesKnownToolCall{}
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
		if native, content, err := messagesNativeContent(message.Role, blocks, knownCalls); err != nil {
			return result, err
		} else if native {
			result.Messages = append(result.Messages, openai.Message{Role: message.Role, NativeContent: content})
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
				if err := decodeMessagesValue(raw, &block); err != nil || message.Role != "assistant" || block.ID == "" || block.Name == "" || block.Input == nil {
					return result, errors.New("invalid or unsupported tool_use block")
				}
				if _, duplicate := knownCalls[block.ID]; duplicate {
					return result, errors.New("invalid or unsupported tool_use block")
				}
				knownCalls[block.ID] = messagesKnownToolCall{ToolName: block.Name}
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
				if err := decodeMessagesValue(raw, &block); err != nil || message.Role != "user" || block.ID == "" || block.IsError {
					return result, errors.New("invalid or unsupported tool_result block")
				}
				if call, known := knownCalls[block.ID]; !known || call.ToolsetName != "" {
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
	clientTools := map[string]bool{}
	clientToolsets := map[string]bool{}
	for _, tool := range request.Tools {
		if !isClientToolsetType(tool.Type) && tool.Configs != nil {
			return result, errors.New("configs is supported only for client toolsets")
		}
		switch tool.Type {
		case "computer_toolset_20260801":
			if clientToolsets["computer"] || tool.Name != "" || tool.InputSchema != nil || tool.Description != "" || tool.MaxUses != nil || tool.UserLocation != nil || len(tool.AllowedDomains) > 0 || len(tool.BlockedDomains) > 0 || tool.ResponseInclusion != "" || tool.UseCache != nil || tool.MaxContentTokens != 0 || tool.MaxCharacters != nil || tool.Citations != nil || tool.DeferLoading || !validToolsetAllowedCallers(tool.AllowedCallers) {
				return result, errors.New("invalid or duplicate computer toolset")
			}
			configs, deferred, err := computerToolsetConfigs(tool.Configs)
			if err != nil {
				return result, err
			}
			var breakpoint *openai.PromptCacheBreakpoint
			if tool.CacheControl != nil {
				if deferred {
					return result, errors.New("deferred computer toolset cannot use cache_control")
				}
				breakpoint, err = messagesPromptCacheBreakpoint(tool.CacheControl)
				if err != nil {
					return result, err
				}
			}
			clientToolsets["computer"] = true
			result.AnthropicClientToolsets = append(result.AnthropicClientToolsets, openai.AnthropicClientToolset{Type: tool.Type, Name: "computer", Configs: configs, AllowedCallers: append([]string(nil), tool.AllowedCallers...), PromptCacheBreakpoint: breakpoint})
			result.NativeInputTokens = openai.ReserveTokens(result.NativeInputTokens, 4590)
			continue
		case "browser_toolset_20260801":
			if clientToolsets["browser"] || tool.Name != "" || tool.InputSchema != nil || tool.Description != "" || tool.MaxUses != nil || tool.UserLocation != nil || len(tool.AllowedDomains) > 0 || len(tool.BlockedDomains) > 0 || tool.ResponseInclusion != "" || tool.UseCache != nil || tool.MaxContentTokens != 0 || tool.MaxCharacters != nil || tool.Citations != nil || tool.DeferLoading || !validToolsetAllowedCallers(tool.AllowedCallers) {
				return result, errors.New("invalid or duplicate browser toolset")
			}
			configs, deferred, err := clientToolsetConfigs("browser", browserToolMembers, browserToolDefaults, tool.Configs)
			if err != nil {
				return result, err
			}
			var breakpoint *openai.PromptCacheBreakpoint
			if tool.CacheControl != nil {
				if deferred {
					return result, errors.New("deferred browser toolset cannot use cache_control")
				}
				breakpoint, err = messagesPromptCacheBreakpoint(tool.CacheControl)
				if err != nil {
					return result, err
				}
			}
			clientToolsets["browser"] = true
			result.AnthropicClientToolsets = append(result.AnthropicClientToolsets, openai.AnthropicClientToolset{Type: tool.Type, Name: "browser", Configs: configs, AllowedCallers: append([]string(nil), tool.AllowedCallers...), PromptCacheBreakpoint: breakpoint})
			reserve := 6670
			for name := range browserOptionalToolMembers {
				if config := configs[name]; config.Enabled != nil && *config.Enabled {
					reserve = 7550
					break
				}
			}
			result.NativeInputTokens = openai.ReserveTokens(result.NativeInputTokens, reserve)
			continue
		case "memory_20250818", "bash_20250124", "text_editor_20250124", "text_editor_20250728":
			capability, expectedName := anthropicClientToolIdentity(tool.Type)
			if clientTools[capability] || tool.Name != expectedName || tool.InputSchema != nil || tool.Description != "" || tool.MaxUses != nil || tool.UserLocation != nil || len(tool.AllowedDomains) > 0 || len(tool.BlockedDomains) > 0 || tool.ResponseInclusion != "" || tool.UseCache != nil || tool.MaxContentTokens != 0 || tool.Citations != nil || !validAnthropicAllowedCallers(tool.AllowedCallers) {
				return result, errors.New("invalid or duplicate client tool")
			}
			if tool.MaxCharacters != nil && (tool.Type != "text_editor_20250728" || *tool.MaxCharacters <= 0 || *tool.MaxCharacters > 1<<20) {
				return result, errors.New("max_characters requires text_editor_20250728 and must be between 1 and 1048576")
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
			clientTools[capability] = true
			result.AnthropicClientTools = append(result.AnthropicClientTools, openai.AnthropicClientTool{Type: tool.Type, Name: tool.Name, AllowedCallers: append([]string(nil), tool.AllowedCallers...), PromptCacheBreakpoint: breakpoint, DeferLoading: tool.DeferLoading, MaxCharacters: tool.MaxCharacters})
			result.NativeInputTokens = openai.ReserveTokens(result.NativeInputTokens, openai.EstimateContextTokens(tool))
			continue
		case "tool_search_tool_regex_20251119", "tool_search_tool_bm25_20251119":
			expectedName := strings.TrimSuffix(tool.Type, "_20251119")
			if toolSearch || tool.Name != expectedName || tool.InputSchema != nil || tool.Description != "" || tool.CacheControl != nil || tool.MaxUses != nil || tool.UserLocation != nil || messagesToolHasNativeWebFields(tool) || tool.MaxContentTokens != 0 || tool.MaxCharacters != nil || tool.Citations != nil || tool.DeferLoading {
				return result, errors.New("invalid or duplicate tool search tool")
			}
			toolSearch = true
			result.AnthropicToolSearch = tool.Type
			result.NativeInputTokens = openai.ReserveTokens(result.NativeInputTokens, openai.EstimateContextTokens(tool))
			continue
		case "code_execution_20250825", "code_execution_20260120", "code_execution_20260521":
			if result.AnthropicCodeExecution || tool.Name != "code_execution" || tool.InputSchema != nil || tool.Description != "" || tool.CacheControl != nil || tool.MaxUses != nil || tool.UserLocation != nil || messagesToolHasNativeWebFields(tool) || tool.MaxContentTokens != 0 || tool.MaxCharacters != nil || tool.Citations != nil || tool.DeferLoading {
				return result, errors.New("invalid or duplicate code execution tool")
			}
			result.AnthropicCodeExecution = true
			result.AnthropicCodeExecutionType = tool.Type
			result.NativeInputTokens = openai.ReserveTokens(result.NativeInputTokens, openai.EstimateContextTokens(tool))
			continue
		case "web_search_20250305", "web_search_20260209", "web_search_20260318":
			location, validLocation := tool.UserLocation.chat()
			if searchTool || tool.Name != "web_search" || tool.InputSchema != nil || tool.Description != "" || tool.CacheControl != nil || tool.MaxContentTokens != 0 || tool.MaxCharacters != nil || tool.Citations != nil || tool.UseCache != nil || tool.DeferLoading || !validLocation {
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
			if fetchTool || tool.Name != "web_fetch" || tool.InputSchema != nil || tool.Description != "" || tool.CacheControl != nil || tool.UserLocation != nil || len(tool.BlockedDomains) > 0 || tool.MaxCharacters != nil || (tool.Citations != nil && !tool.Citations.Enabled) || tool.DeferLoading {
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
		if tool.Name == "" || tool.InputSchema == nil || tool.MaxUses != nil || tool.UserLocation != nil || messagesToolHasNativeWebFields(tool) || tool.MaxContentTokens != 0 || tool.MaxCharacters != nil || tool.Citations != nil {
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
	for _, tool := range result.AnthropicClientTools {
		if tool.DeferLoading && !toolSearch {
			return result, errors.New("defer_loading requires a tool search tool")
		}
	}
	for _, message := range result.Messages {
		for _, raw := range message.NativeContent {
			var block struct {
				ToolsetName string `json:"toolset_name"`
			}
			if json.Unmarshal(raw, &block) == nil && block.ToolsetName != "" && !clientToolsets[block.ToolsetName] {
				return result, errors.New("client toolset history requires the matching toolset")
			}
		}
	}
	if len(clientToolsets) > 0 {
		customNames := make(map[string]bool, len(result.Tools))
		for _, tool := range result.Tools {
			customNames[tool.Function.Name] = true
		}
		for _, message := range result.Messages {
			for _, call := range message.ToolCalls {
				if clientToolsetMemberName(call.Function.Name, clientToolsets) && !customNames[call.Function.Name] {
					return result, errors.New("client toolset calls require toolset_name")
				}
			}
		}
	}
	for _, toolset := range result.AnthropicClientToolsets {
		if toolsetDeferred(toolset) && !toolSearch {
			return result, errors.New("deferred client toolset requires a tool search tool")
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
			if clientToolsets[choice.Name] || clientToolsetMemberName(choice.Name, clientToolsets) {
				return result, errors.New("tool_choice cannot select a client toolset member")
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

func anthropicClientToolIdentity(toolType string) (string, string) {
	switch toolType {
	case "memory_20250818":
		return "memory_tool", "memory"
	case "bash_20250124":
		return "bash_tool", "bash"
	case "text_editor_20250124":
		return "text_editor_tool", "str_replace_editor"
	case "text_editor_20250728":
		return "text_editor_tool", "str_replace_based_edit_tool"
	default:
		return "", ""
	}
}

func validAnthropicAllowedCallers(callers []string) bool {
	if len(callers) > 4 {
		return false
	}
	seen := make(map[string]bool, len(callers))
	for _, caller := range callers {
		switch caller {
		case "direct", "code_execution_20250825", "code_execution_20260120", "code_execution_20260521":
		default:
			return false
		}
		if seen[caller] {
			return false
		}
		seen[caller] = true
	}
	return true
}

func anthropicClientToolIdentifiers(tools []openai.AnthropicClientTool) []string {
	identifiers := make([]string, 0, len(tools))
	for _, tool := range tools {
		identifiers = append(identifiers, tool.Name)
	}
	return identifiers
}

var computerToolMembers = map[string]bool{
	"screenshot": true, "zoom": true, "left_click": true, "right_click": true, "middle_click": true,
	"double_click": true, "triple_click": true, "left_click_drag": true, "mouse_move": true,
	"left_mouse_down": true, "left_mouse_up": true, "cursor_position": true, "scroll": true,
	"type": true, "key": true, "hold_key": true, "wait": true,
}

var browserToolMembers = map[string]bool{
	"navigate": true, "screenshot": true, "zoom": true, "left_click": true, "right_click": true,
	"middle_click": true, "double_click": true, "triple_click": true, "hover": true,
	"left_click_drag": true, "left_mouse_down": true, "left_mouse_up": true, "mouse_move": true,
	"scroll": true, "scroll_to": true, "type": true, "key": true, "hold_key": true, "wait": true,
	"read_page": true, "find": true, "get_page_text": true, "form_input": true, "file_upload": true,
	"read_console": true, "read_network": true, "javascript_exec": true, "new_tab": true,
	"list_tabs": true, "switch_tab": true, "close_tab": true,
}

var browserOptionalToolMembers = map[string]bool{
	"file_upload": true, "read_console": true, "read_network": true, "javascript_exec": true,
}

var browserToolDefaults = func() map[string]bool {
	defaults := make(map[string]bool, len(browserToolMembers))
	for name := range browserToolMembers {
		defaults[name] = !browserOptionalToolMembers[name]
	}
	return defaults
}()

func isClientToolsetType(toolType string) bool {
	return toolType == "computer_toolset_20260801" || toolType == "browser_toolset_20260801"
}

func clientToolsetMembers(toolsetName string) map[string]bool {
	switch toolsetName {
	case "computer":
		return computerToolMembers
	case "browser":
		return browserToolMembers
	default:
		return nil
	}
}

func clientToolsetMemberName(name string, enabledToolsets map[string]bool) bool {
	for toolsetName := range enabledToolsets {
		if clientToolsetMembers(toolsetName)[name] {
			return true
		}
	}
	return false
}

func validToolsetAllowedCallers(callers []string) bool {
	return len(callers) == 0 || len(callers) == 1 && callers[0] == "direct"
}

func computerToolsetConfigs(configs map[string]messagesToolsetMemberConfig) (map[string]openai.AnthropicToolsetMemberConfig, bool, error) {
	defaults := make(map[string]bool, len(computerToolMembers))
	for name := range computerToolMembers {
		defaults[name] = true
	}
	return clientToolsetConfigs("computer", computerToolMembers, defaults, configs)
}

func clientToolsetConfigs(toolsetName string, members, defaults map[string]bool, configs map[string]messagesToolsetMemberConfig) (map[string]openai.AnthropicToolsetMemberConfig, bool, error) {
	converted := make(map[string]openai.AnthropicToolsetMemberConfig, len(configs))
	enabledCount := 0
	deferred := false
	deferSet := false
	for name := range members {
		config, configured := configs[name]
		enabled := defaults[name]
		if configured && config.Enabled != nil {
			enabled = *config.Enabled
		}
		if enabled {
			enabledCount++
			memberDeferred := configured && config.DeferLoading != nil && *config.DeferLoading
			if !deferSet {
				deferred, deferSet = memberDeferred, true
			} else if memberDeferred != deferred {
				return nil, false, fmt.Errorf("all enabled %s toolset members must use the same defer_loading value", toolsetName)
			}
		}
	}
	for name, config := range configs {
		if !members[name] {
			return nil, false, fmt.Errorf("unknown %s toolset member %q", toolsetName, name)
		}
		converted[name] = openai.AnthropicToolsetMemberConfig{Enabled: config.Enabled, DeferLoading: config.DeferLoading}
	}
	if enabledCount == 0 {
		return nil, false, fmt.Errorf("%s toolset must enable at least one member", toolsetName)
	}
	return converted, deferred, nil
}

func toolsetDeferred(toolset openai.AnthropicClientToolset) bool {
	members := clientToolsetMembers(toolset.Name)
	defaults := browserToolDefaults
	if toolset.Name == "computer" {
		defaults = make(map[string]bool, len(members))
		for name := range members {
			defaults[name] = true
		}
	}
	for name := range members {
		config := toolset.Configs[name]
		enabled := defaults[name]
		if config.Enabled != nil {
			enabled = *config.Enabled
		}
		if enabled {
			return config.DeferLoading != nil && *config.DeferLoading
		}
	}
	return false
}

func anthropicClientToolsetIdentifiers(toolsets []openai.AnthropicClientToolset) []string {
	var identifiers []string
	for _, toolset := range toolsets {
		members := clientToolsetMembers(toolset.Name)
		if members == nil {
			continue
		}
		defaults := browserToolDefaults
		if toolset.Name == "computer" {
			defaults = make(map[string]bool, len(members))
			for name := range members {
				defaults[name] = true
			}
		}
		for name := range members {
			config := toolset.Configs[name]
			enabled := defaults[name]
			if config.Enabled != nil {
				enabled = *config.Enabled
			}
			if enabled {
				identifiers = append(identifiers, toolset.Name+":"+name)
			}
		}
	}
	slices.Sort(identifiers)
	return identifiers
}

func messagesToolHasNativeWebFields(tool messagesTool) bool {
	return len(tool.AllowedDomains) > 0 || len(tool.BlockedDomains) > 0 || len(tool.AllowedCallers) > 0 || tool.ResponseInclusion != "" || tool.UseCache != nil
}

func messagesNativeContent(role string, blocks []json.RawMessage, knownCalls map[string]messagesKnownToolCall) (bool, []json.RawMessage, error) {
	for _, raw := range blocks {
		var marker struct {
			ToolsetName string `json:"toolset_name"`
		}
		if json.Unmarshal(raw, &marker) == nil && marker.ToolsetName != "" {
			return messagesNativeClientToolsetContent(role, blocks, knownCalls)
		}
	}
	return messagesNativeAssistantContent(role, blocks, knownCalls)
}

func messagesNativeClientToolsetContent(role string, blocks []json.RawMessage, knownCalls map[string]messagesKnownToolCall) (bool, []json.RawMessage, error) {
	if len(blocks) > 128 || role != "assistant" && role != "user" {
		return true, nil, errors.New("client toolset content requires a bounded message block array")
	}
	result := make([]json.RawMessage, 0, len(blocks))
	serverCalls := map[string]string{}
	total := 0
	for _, raw := range blocks {
		if !json.Valid(raw) || len(raw) > 4<<20 || total > (32<<20)-len(raw) {
			return true, nil, errors.New("client toolset content exceeds its size limit")
		}
		total += len(raw)
		var kind struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &kind) != nil {
			return true, nil, errors.New("invalid client toolset content")
		}
		switch kind.Type {
		case "text":
			var block struct {
				Type         string                `json:"type"`
				Text         string                `json:"text"`
				CacheControl *messagesCacheControl `json:"cache_control,omitempty"`
			}
			if decodeMessagesValue(raw, &block) != nil {
				return true, nil, errors.New("invalid client toolset text block")
			}
			if block.CacheControl != nil {
				if _, err := messagesPromptCacheBreakpoint(block.CacheControl); err != nil {
					return true, nil, err
				}
			}
		case "thinking", "redacted_thinking":
			if role != "assistant" {
				return true, nil, errors.New("client toolset reasoning requires assistant role")
			}
			var block map[string]any
			if json.Unmarshal(raw, &block) != nil || validateNativeMessageBlock(block) != nil {
				return true, nil, errors.New("invalid client toolset reasoning block")
			}
		case "tool_use":
			var block struct {
				Type        string         `json:"type"`
				ID          string         `json:"id"`
				Name        string         `json:"name"`
				ToolsetName string         `json:"toolset_name,omitempty"`
				Input       map[string]any `json:"input"`
			}
			if decodeMessagesValue(raw, &block) != nil || role != "assistant" || block.ID == "" || block.Name == "" || block.Input == nil {
				return true, nil, errors.New("invalid client toolset tool_use block")
			}
			members := clientToolsetMembers(block.ToolsetName)
			if block.ToolsetName != "" && (members == nil || !members[block.Name]) {
				return true, nil, errors.New("invalid client toolset tool_use block")
			}
			if _, duplicate := knownCalls[block.ID]; duplicate {
				return true, nil, errors.New("invalid client toolset tool_use block")
			}
			knownCalls[block.ID] = messagesKnownToolCall{ToolsetName: block.ToolsetName, ToolName: block.Name}
		case "server_tool_use":
			var block struct {
				Type  string         `json:"type"`
				ID    string         `json:"id"`
				Name  string         `json:"name"`
				Input map[string]any `json:"input"`
			}
			if decodeMessagesValue(raw, &block) != nil || role != "assistant" || block.ID == "" || block.Name == "" || block.Input == nil || serverCalls[block.ID] != "" {
				return true, nil, errors.New("invalid server_tool_use block")
			}
			serverCalls[block.ID] = block.Name
		case "tool_search_tool_result", "web_search_tool_result", "web_fetch_tool_result", "code_execution_tool_result", "bash_code_execution_tool_result", "text_editor_code_execution_tool_result":
			var block map[string]json.RawMessage
			if json.Unmarshal(raw, &block) != nil || role != "assistant" {
				return true, nil, errors.New("invalid server tool result block")
			}
			var id string
			if json.Unmarshal(block["tool_use_id"], &id) != nil || id == "" || serverCalls[id] == "" || block["content"] == nil {
				return true, nil, errors.New("invalid server tool result block")
			}
			delete(serverCalls, id)
		case "tool_result":
			var block struct {
				Type         string                `json:"type"`
				ID           string                `json:"tool_use_id"`
				ToolsetName  string                `json:"toolset_name"`
				Content      json.RawMessage       `json:"content"`
				IsError      bool                  `json:"is_error,omitempty"`
				CacheControl *messagesCacheControl `json:"cache_control,omitempty"`
			}
			if decodeMessagesValue(raw, &block) != nil || role != "user" || block.ID == "" {
				return true, nil, errors.New("invalid client toolset tool_result block")
			}
			expected, known := knownCalls[block.ID]
			if !known || block.ToolsetName != expected.ToolsetName {
				return true, nil, errors.New("invalid client toolset tool_result block")
			}
			if block.ToolsetName == "computer" {
				if validateComputerToolResultContent(block.Content) != nil {
					return true, nil, errors.New("invalid computer toolset tool_result block")
				}
			} else if block.ToolsetName == "browser" {
				if validateBrowserToolResultContent(block.Content, block.IsError, expected.ToolName) != nil {
					return true, nil, errors.New("invalid browser toolset tool_result block")
				}
			} else if block.ToolsetName != "" || block.IsError {
				return true, nil, errors.New("invalid client toolset tool_result block")
			} else if _, err := messagesText(block.Content); err != nil {
				return true, nil, errors.New("invalid tool_result block")
			}
			if block.CacheControl != nil {
				if _, err := messagesPromptCacheBreakpoint(block.CacheControl); err != nil {
					return true, nil, err
				}
			}
			delete(knownCalls, block.ID)
		default:
			return true, nil, fmt.Errorf("unsupported client toolset content block type %q", kind.Type)
		}
		result = append(result, append(json.RawMessage(nil), raw...))
	}
	if len(serverCalls) > 0 {
		return true, nil, errors.New("server_tool_use requires a matching result block")
	}
	return true, result, nil
}

func validateComputerToolResultContent(raw json.RawMessage) error {
	var text string
	if json.Unmarshal(raw, &text) == nil && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil || len(blocks) == 0 || len(blocks) > 128 {
		return errors.New("tool result content must be text or text/image blocks")
	}
	for _, rawBlock := range blocks {
		var kind struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(rawBlock, &kind) != nil {
			return errors.New("invalid tool result content")
		}
		switch kind.Type {
		case "text":
			var block struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if decodeMessagesValue(rawBlock, &block) != nil {
				return errors.New("invalid tool result text")
			}
		case "image":
			var block struct {
				Type   string `json:"type"`
				Source struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			}
			if decodeMessagesValue(rawBlock, &block) != nil || block.Source.Type != "base64" {
				return errors.New("invalid tool result image")
			}
			if _, err := openai.ParseDataImageURL("data:" + block.Source.MediaType + ";base64," + block.Source.Data); err != nil {
				return err
			}
		default:
			return errors.New("computer tool results support only text and image blocks")
		}
	}
	return nil
}

func validateBrowserToolResultContent(raw json.RawMessage, isError bool, toolName string) error {
	var text string
	if json.Unmarshal(raw, &text) == nil && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if !isError && browserTabToolMembers[toolName] {
			return errors.New("tab tool results require browser_state")
		}
		return nil
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil || len(blocks) == 0 || len(blocks) > 128 {
		return errors.New("browser tool result content must be text or content blocks")
	}
	textCount, imageCount, stateCount := 0, 0, 0
	var state browserStateBlock
	for _, rawBlock := range blocks {
		var kind struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(rawBlock, &kind) != nil {
			return errors.New("invalid browser tool result content")
		}
		switch kind.Type {
		case "text":
			var block struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if decodeMessagesValue(rawBlock, &block) != nil {
				return errors.New("invalid browser tool result text")
			}
			textCount++
		case "image":
			var block struct {
				Type   string `json:"type"`
				Source struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			}
			if decodeMessagesValue(rawBlock, &block) != nil || block.Source.Type != "base64" {
				return errors.New("invalid browser tool result image")
			}
			if _, err := openai.ParseDataImageURL("data:" + block.Source.MediaType + ";base64," + block.Source.Data); err != nil {
				return err
			}
			imageCount++
		case "browser_state":
			if stateCount > 0 || decodeMessagesValue(rawBlock, &state) != nil || validateBrowserState(state) != nil {
				return errors.New("invalid browser_state block")
			}
			stateCount++
		default:
			return errors.New("browser tool results support only text, image and browser_state blocks")
		}
	}
	if isError {
		if textCount == 0 || imageCount != 0 || stateCount != 0 {
			return errors.New("browser tool errors require text without browser state")
		}
		return nil
	}
	if browserTabToolMembers[toolName] {
		if len(blocks) != 1 || stateCount != 1 {
			return errors.New("tab tool results require exactly one browser_state block")
		}
		if toolName == "new_tab" && !validNewTabState(state) {
			return errors.New("new_tab result requires one matching tab_opened change")
		}
		return nil
	}
	if (toolName == "screenshot" || toolName == "zoom") && imageCount == 0 {
		return errors.New("browser capture results require an image")
	}
	if toolName != "screenshot" && toolName != "zoom" && textCount == 0 {
		return errors.New("browser tool result requires text")
	}
	return nil
}

var browserTabToolMembers = map[string]bool{"new_tab": true, "list_tabs": true, "switch_tab": true, "close_tab": true}

type browserStateBlock struct {
	Type         string               `json:"type"`
	Tabs         []browserStateTab    `json:"tabs"`
	StateChanges []browserStateChange `json:"state_changes,omitempty"`
}

type browserStateTab struct {
	TabID  string `json:"tab_id"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Active bool   `json:"active,omitempty"`
}

type browserStateChange struct {
	Type       string `json:"type"`
	TabID      string `json:"tab_id,omitempty"`
	DownloadID string `json:"download_id,omitempty"`
	URL        string `json:"url,omitempty"`
	Path       string `json:"path,omitempty"`
	SizeBytes  *int64 `json:"size_bytes,omitempty"`
	Error      string `json:"error,omitempty"`
}

func validateBrowserState(state browserStateBlock) error {
	if state.Type != "browser_state" || len(state.Tabs) > 100 || len(state.StateChanges) > 200 || state.StateChanges != nil && len(state.StateChanges) == 0 {
		return errors.New("invalid browser state shape")
	}
	tabs := make(map[string]bool, len(state.Tabs))
	active := 0
	for _, tab := range state.Tabs {
		if !validBrowserRenderedString(tab.TabID, true) || !validBrowserRenderedString(tab.Title, false) || !validBrowserRenderedString(tab.URL, false) || tabs[tab.TabID] {
			return errors.New("invalid browser tab")
		}
		tabs[tab.TabID] = true
		if tab.Active {
			active++
		}
	}
	if len(state.Tabs) > 0 && active != 1 {
		return errors.New("browser state requires one active tab")
	}
	for _, change := range state.StateChanges {
		switch change.Type {
		case "tab_opened":
			if !validBrowserRenderedString(change.TabID, true) || !tabs[change.TabID] || change.DownloadID != "" || change.URL != "" || change.Path != "" || change.SizeBytes != nil || change.Error != "" {
				return errors.New("invalid tab_opened change")
			}
		case "download_started":
			if !validBrowserDownloadChange(change) || change.Path != "" || change.SizeBytes != nil || change.Error != "" {
				return errors.New("invalid download_started change")
			}
		case "download_completed":
			if !validBrowserDownloadChange(change) || !validBrowserRenderedString(change.Path, false) || change.SizeBytes != nil && *change.SizeBytes < 0 || change.Error != "" {
				return errors.New("invalid download_completed change")
			}
		case "download_failed":
			if !validBrowserDownloadChange(change) || change.Path != "" || change.SizeBytes != nil || !validBrowserRenderedString(change.Error, false) {
				return errors.New("invalid download_failed change")
			}
		default:
			return errors.New("unsupported browser state change")
		}
	}
	return nil
}

func validBrowserDownloadChange(change browserStateChange) bool {
	return validBrowserRenderedString(change.DownloadID, true) && validBrowserRenderedString(change.URL, true) && change.TabID == ""
}

func validBrowserRenderedString(value string, required bool) bool {
	if required && value == "" || utf8.RuneCountInString(value) > 4096 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}

func validNewTabState(state browserStateBlock) bool {
	if len(state.StateChanges) != 1 || state.StateChanges[0].Type != "tab_opened" {
		return false
	}
	for _, tab := range state.Tabs {
		if tab.Active {
			return tab.TabID == state.StateChanges[0].TabID
		}
	}
	return false
}

func messagesNativeAssistantContent(role string, blocks []json.RawMessage, knownCalls map[string]messagesKnownToolCall) (bool, []json.RawMessage, error) {
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
			if json.Unmarshal(block["id"], &id) != nil || json.Unmarshal(block["name"], &name) != nil || json.Unmarshal(block["input"], &input) != nil || id == "" || name == "" {
				return true, nil, errors.New("invalid native tool_use block")
			}
			if _, duplicate := knownCalls[id]; duplicate {
				return true, nil, errors.New("invalid native tool_use block")
			}
			knownCalls[id] = messagesKnownToolCall{}
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
