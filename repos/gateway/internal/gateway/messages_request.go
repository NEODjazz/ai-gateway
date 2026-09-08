package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

type messagesRequest struct {
	Model         string              `json:"model"`
	MaxTokens     int                 `json:"max_tokens"`
	Messages      []messagesInput     `json:"messages"`
	System        json.RawMessage     `json:"system,omitempty"`
	Tools         []messagesTool      `json:"tools,omitempty"`
	ToolChoice    *messagesToolChoice `json:"tool_choice,omitempty"`
	Temperature   *float64            `json:"temperature,omitempty"`
	TopP          *float64            `json:"top_p,omitempty"`
	Stream        bool                `json:"stream,omitempty"`
	StopSequences []string            `json:"stop_sequences,omitempty"`
}
type messagesInput struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}
type messagesTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}
type messagesToolChoice struct {
	Type            string `json:"type"`
	Name            string `json:"name,omitempty"`
	DisableParallel *bool  `json:"disable_parallel_tool_use,omitempty"`
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
	result := openai.ChatCompletionRequest{Model: request.Model, MaxTokens: &request.MaxTokens, Temperature: request.Temperature, TopP: request.TopP, Stream: request.Stream}
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
		content, err := messagesText(request.System)
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
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if err := decodeMessagesValue(raw, &block); err != nil {
					return result, fmt.Errorf("text block: %w", err)
				}
				parts = append(parts, map[string]any{"type": "text", "text": block.Text})
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
	if request.Messages[len(request.Messages)-1].Role == "assistant" {
		return result, errors.New("assistant prefill is not supported")
	}
	if len(knownCalls) > 0 {
		return result, errors.New("all tool_use blocks require a tool_result")
	}
	if len(request.Tools) > 128 {
		return result, errors.New("too many tools")
	}
	for _, tool := range request.Tools {
		if tool.Name == "" || tool.InputSchema == nil {
			return result, errors.New("tools require name and input_schema")
		}
		result.Tools = append(result.Tools, openai.Tool{Type: "function", Function: openai.FunctionDefinition{Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema}})
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
