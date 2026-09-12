package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ai-gateway-gateway/internal/openai"
)

// TokenCountClient counts context using the provider's native counter. It does
// not execute generation or substitute a local estimate after a provider error.
type TokenCountClient interface {
	CountTokens(context.Context, TokenCountRequest) (TokenCountResult, error)
}
type TokenCountRequest struct {
	Model                      string
	Messages                   []openai.Message
	Tools                      []openai.Tool
	ToolChoice                 any
	ParallelToolCalls          *bool
	ChatGenerationOptions      openai.ChatGenerationOptions
	ResponseFormat             *openai.ResponseFormat
	AnthropicSkills            []openai.AnthropicSkillReference
	AnthropicContainerID       string
	AnthropicCodeExecution     bool
	AnthropicCodeExecutionType string
	AnthropicToolSearch        string
	AnthropicClientTools       []openai.AnthropicClientTool
	AnthropicClientToolsets    []openai.AnthropicClientToolset
	AnthropicThinking          *openai.AnthropicThinkingConfig
	AnthropicCacheControl      *openai.PromptCacheBreakpoint
	AnthropicInferenceGeo      string
	AnthropicContextManagement json.RawMessage
}
type TokenCountResult struct {
	InputTokens         int
	OriginalInputTokens *int
	Model               string
	Source              string
}

func (p Anthropic) CountTokens(ctx context.Context, request TokenCountRequest) (TokenCountResult, error) {
	chat := openai.ChatCompletionRequest{Model: request.Model, Messages: request.Messages, Tools: request.Tools, ToolChoice: request.ToolChoice, ParallelToolCalls: request.ParallelToolCalls, ChatGenerationOptions: request.ChatGenerationOptions, ResponseFormat: request.ResponseFormat, AnthropicSkills: request.AnthropicSkills, AnthropicContainerID: request.AnthropicContainerID, AnthropicCodeExecution: request.AnthropicCodeExecution, AnthropicCodeExecutionType: request.AnthropicCodeExecutionType, AnthropicToolSearch: request.AnthropicToolSearch, AnthropicClientTools: request.AnthropicClientTools, AnthropicClientToolsets: request.AnthropicClientToolsets, AnthropicThinking: request.AnthropicThinking, AnthropicCacheControl: request.AnthropicCacheControl, AnthropicInferenceGeo: request.AnthropicInferenceGeo, AnthropicContextManagement: request.AnthropicContextManagement}
	if err := validateTokenCountRequest(chat); err != nil {
		return TokenCountResult{}, err
	}
	if err := p.ValidateChatParameters(chat); err != nil {
		return TokenCountResult{}, err
	}
	native := anthropicChatRequest(chat, false)
	body, err := json.Marshal(struct {
		Model             string                 `json:"model"`
		System            any                    `json:"system,omitempty"`
		Messages          []anthropicMessage     `json:"messages"`
		Tools             []anthropicTool        `json:"tools,omitempty"`
		ToolChoice        map[string]any         `json:"tool_choice,omitempty"`
		OutputConfig      *anthropicOutputConfig `json:"output_config,omitempty"`
		Thinking          *anthropicThinking     `json:"thinking,omitempty"`
		Container         *anthropicContainer    `json:"container,omitempty"`
		CacheControl      *anthropicCacheControl `json:"cache_control,omitempty"`
		InferenceGeo      string                 `json:"inference_geo,omitempty"`
		ContextManagement json.RawMessage        `json:"context_management,omitempty"`
	}{
		Model: native.Model, System: native.System, Messages: native.Messages,
		Tools: native.Tools, ToolChoice: native.ToolChoice, OutputConfig: native.OutputConfig,
		Thinking: native.Thinking, Container: native.Container, CacheControl: native.CacheControl,
		InferenceGeo:      native.InferenceGeo,
		ContextManagement: native.ContextManagement,
	})
	if err != nil {
		return TokenCountResult{}, err
	}
	if len(body) > openai.MaxInferenceBodyBytes {
		return TokenCountResult{}, errors.New("token count request exceeds inference limit")
	}
	base, err := url.Parse(p.baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return TokenCountResult{}, errors.New("invalid Anthropic base URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "messages/count_tokens"), bytes.NewReader(body))
	if err != nil {
		return TokenCountResult{}, err
	}
	p.setHeaders(req)
	if beta := anthropicBetaFeatures(native); beta != "" {
		req.Header.Set("anthropic-beta", beta)
	}
	client := *p.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return TokenCountResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return TokenCountResult{}, responseStatusError("anthropic", response)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil {
		return TokenCountResult{}, err
	}
	if len(payload) > 64<<10 {
		return TokenCountResult{}, errors.New("token count response exceeds limit")
	}
	var result struct {
		InputTokens       *int `json:"input_tokens"`
		ContextManagement *struct {
			OriginalInputTokens *int `json:"original_input_tokens"`
		} `json:"context_management,omitempty"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return TokenCountResult{}, err
	}
	if result.InputTokens == nil || *result.InputTokens < 0 {
		return TokenCountResult{}, errors.New("invalid provider token count")
	}
	var original *int
	if result.ContextManagement != nil {
		if result.ContextManagement.OriginalInputTokens == nil || *result.ContextManagement.OriginalInputTokens < *result.InputTokens {
			return TokenCountResult{}, errors.New("invalid provider context management token count")
		}
		value := *result.ContextManagement.OriginalInputTokens
		original = &value
	}
	return TokenCountResult{InputTokens: *result.InputTokens, OriginalInputTokens: original, Model: request.Model, Source: "anthropic"}, nil
}

func validateTokenCountRequest(request openai.ChatCompletionRequest) error {
	invalid := func(field string) error { return rejectParameters("provider", parameterCheck{field, true}) }
	if _, message := openai.ChatRequestPromptCacheBreakpoints(request); message != "" {
		return invalid("prompt_cache_breakpoint")
	}
	if strings.TrimSpace(request.Model) == "" || len(request.Messages) == 0 || len(request.Messages) > 10000 || len(request.Tools) > 128 {
		return invalid("messages")
	}
	if _, err := openai.ChatImageAttachments(request.Messages); err != nil {
		return err
	}
	if _, err := openai.ChatAudioAttachments(request.Messages); err != nil {
		return err
	}
	if _, err := openai.ChatFileAttachments(request.Messages); err != nil {
		return err
	}
	if _, err := openai.ChatVideoAttachments(request.Messages); err != nil {
		return err
	}
	hasConversation := false
	for _, message := range request.Messages {
		if message.Role != "system" && message.Role != "developer" {
			hasConversation = true
		}
		switch message.Role {
		case "user", "assistant", "system", "developer", "tool":
		default:
			return invalid("messages.role")
		}
		if message.Name != "" {
			return invalid("messages.name")
		}
		if message.Role != "assistant" && len(message.ToolCalls) > 0 {
			return invalid("messages.tool_calls")
		}
		if message.Role != "tool" && message.ToolCallID != "" {
			return invalid("messages.tool_call_id")
		}
		if message.Role == "tool" && message.ToolCallID == "" {
			return invalid("messages.tool_call_id")
		}
		switch content := message.Content.(type) {
		case nil, string:
		case []any:
			for _, raw := range content {
				part, ok := raw.(map[string]any)
				if !ok {
					return invalid("messages.content")
				}
				switch part["type"] {
				case "text":
					_, hasBreakpoint := part["prompt_cache_breakpoint"]
					if _, ok := part["text"].(string); !ok || len(part) != 2 && !(len(part) == 3 && hasBreakpoint) {
						return invalid("messages.content")
					}
				case "image_url":
					image, ok := part["image_url"].(map[string]any)
					if !ok || len(part) != 2 || len(image) != 1 {
						return invalid("messages.content")
					}
				default:
					return invalid("messages.content")
				}
			}
		default:
			return invalid("messages.content")
		}
		for _, call := range message.ToolCalls {
			var args map[string]any
			if call.ID == "" || call.Type != "function" || call.Function.Name == "" || json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args == nil {
				return invalid("messages.tool_calls")
			}
		}
	}
	if !hasConversation {
		return invalid("messages")
	}
	for _, tool := range request.Tools {
		if tool.Type != "function" || tool.Function.Name == "" || tool.Function.Parameters == nil || (tool.Function.Strict != nil && *tool.Function.Strict) {
			return invalid("tools")
		}
	}
	switch choice := request.ToolChoice.(type) {
	case nil:
	case string:
		if choice != "auto" && choice != "none" && choice != "required" {
			return invalid("tool_choice")
		}
	case map[string]any:
		function, ok := choice["function"].(map[string]any)
		name, _ := function["name"].(string)
		if !ok || choice["type"] != "function" || len(choice) != 2 || len(function) != 1 || name == "" {
			return invalid("tool_choice")
		}
	default:
		return invalid("tool_choice")
	}
	return nil
}
