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
	Temperature   *float64              `json:"temperature,omitempty"`
	TopP          *float64              `json:"top_p,omitempty"`
	Stream        bool                  `json:"stream,omitempty"`
	StopSequences []string              `json:"stop_sequences,omitempty"`
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
	Name         string                `json:"name"`
	Description  string                `json:"description,omitempty"`
	InputSchema  map[string]any        `json:"input_schema"`
	CacheControl *messagesCacheControl `json:"cache_control,omitempty"`
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
		converted := openai.Message{Role: message.Role}
		parts := []any{}
		flush := func() {
			if len(parts) > 0 || len(converted.ToolCalls) > 0 {
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
	for _, tool := range request.Tools {
		if tool.Name == "" || tool.InputSchema == nil {
			return result, errors.New("tools require name and input_schema")
		}
		var breakpoint *openai.PromptCacheBreakpoint
		if tool.CacheControl != nil {
			var err error
			breakpoint, err = messagesPromptCacheBreakpoint(tool.CacheControl)
			if err != nil {
				return result, err
			}
		}
		result.Tools = append(result.Tools, openai.Tool{Type: "function", Function: openai.FunctionDefinition{Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema, PromptCacheBreakpoint: breakpoint}})
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
