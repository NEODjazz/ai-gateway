package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"unicode/utf8"

	"ai-gateway-gateway/internal/openai"
)

type DeepSeek struct {
	compatible OpenAICompatible
}

func NewDeepSeek(baseURL, apiKey string, stream bool) DeepSeek {
	compatible := NewOpenAICompatible(baseURL, apiKey, stream)
	compatible.errorProvider = "deepseek"
	return DeepSeek{compatible: compatible}
}

func (DeepSeek) SupportsResponses() bool        { return true }
func (DeepSeek) SupportsTools() bool            { return true }
func (DeepSeek) SupportsStructuredOutput() bool { return true }
func (DeepSeek) SupportsVision() bool           { return true }

func (d DeepSeek) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if err := rejectChatModeration("deepseek", request); err != nil {
		return err
	}
	if err := rejectLegacyFunctionCalling("deepseek", request); err != nil {
		return err
	}
	if err := validateChatMessagePrefix("deepseek", request.Messages, false); err != nil {
		return err
	}
	if err := validateChatPromptCacheBreakpoints("deepseek", request, false); err != nil {
		return err
	}
	if err := rejectToolCallMetadata("deepseek", request.Messages); err != nil {
		return err
	}
	if err := rejectChatMessageRefusals("deepseek", request.Messages); err != nil {
		return err
	}
	if err := rejectChatMessageAudio("deepseek", request.Messages); err != nil {
		return err
	}
	if err := validateDeepSeekUser(request.User); err != nil {
		return err
	}
	if err := validateDeepSeekStop(request.Stop); err != nil {
		return err
	}
	if format := request.ResponseFormat; format != nil && (format.Type != "text" && format.Type != "json_object" || format.JSONSchema != nil) {
		return unsupportedDeepSeekParameter("response_format")
	}
	for _, tool := range request.Tools {
		if tool.Type != "function" {
			return unsupportedDeepSeekParameter("tools.type")
		}
	}
	if err := rejectParameters("deepseek",
		parameterCheck{"metadata", request.Metadata != nil}, parameterCheck{"store", request.Store != nil},
		parameterCheck{"modalities", request.Modalities != nil}, parameterCheck{"audio", request.Audio != nil},
		parameterCheck{"safe_prompt", request.SafePrompt != nil},
		parameterCheck{"n", request.N != nil && *request.N != 1}, parameterCheck{"safety_identifier", request.SafetyIdentifier != ""},
		parameterCheck{"prompt_cache_key", request.PromptCacheKey != ""}, parameterCheck{"prompt_cache_options", request.PromptCacheOptions != nil},
		parameterCheck{"prompt_cache_retention", request.PromptCacheRetention != ""}, parameterCheck{"prompt_mode", request.PromptMode != ""},
		parameterCheck{"prediction", request.Prediction != nil}, parameterCheck{"service_tier", request.ServiceTier != ""},
		parameterCheck{"verbosity", request.Verbosity != ""}, parameterCheck{"web_search_options", request.WebSearchOptions != nil},
		parameterCheck{"web_fetch_options", request.WebFetchOptions != nil}, parameterCheck{"frequency_penalty", request.FrequencyPenalty != nil},
		parameterCheck{"presence_penalty", request.PresencePenalty != nil}, parameterCheck{"logit_bias", request.LogitBias != nil},
		parameterCheck{"min_p", request.MinP != nil}, parameterCheck{"top_k", request.TopK != nil}, parameterCheck{"top_a", request.TopA != nil},
		parameterCheck{"repetition_penalty", request.RepetitionPenalty != nil},
		parameterCheck{"parallel_tool_calls", request.ParallelToolCalls != nil}, parameterCheck{"seed", request.Seed != nil},
	); err != nil {
		return err
	}
	if err := validateDeepSeekThinking(request); err != nil {
		return err
	}
	return d.compatible.ValidateChatParameters(request)
}

func validateDeepSeekThinking(request openai.ChatCompletionRequest) error {
	invalid := func(parameter, message string) error {
		return &Error{Class: FailureClientRequest, Provider: "deepseek", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: parameter, Err: errors.New(message)}
	}
	switch request.ReasoningEffort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		return invalid("reasoning_effort", "reasoning_effort is not supported by DeepSeek")
	}
	thinkingEnabled := request.ReasoningEffort != "" && request.ReasoningEffort != "none"
	if request.Thinking != nil {
		thinkingEnabled = request.Thinking.Type == "enabled"
		if !thinkingEnabled && request.ReasoningEffort != "" && request.ReasoningEffort != "none" {
			return invalid("reasoning_effort", "enabled reasoning_effort conflicts with thinking.type=disabled")
		}
		if thinkingEnabled && request.ReasoningEffort == "none" {
			return invalid("reasoning_effort", "reasoning_effort=none conflicts with thinking.type=enabled")
		}
	}
	if !thinkingEnabled {
		if request.TopP != nil {
			return invalid("top_p", "top_p has no effect when DeepSeek thinking mode is disabled")
		}
		return nil
	}
	if request.Temperature != nil {
		return invalid("temperature", "temperature is not supported in DeepSeek thinking mode")
	}
	if request.TopP != nil && (*request.TopP < 0.95 || *request.TopP > 1) {
		return invalid("top_p", "top_p must be between 0.95 and 1 in DeepSeek thinking mode")
	}
	if request.ToolChoice != nil {
		choice, ok := request.ToolChoice.(string)
		if !ok || choice != "auto" && choice != "none" {
			return invalid("tool_choice", "required and named tool choices are not supported in DeepSeek thinking mode")
		}
	}
	return nil
}

func (d DeepSeek) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if err := d.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if request.N != nil {
		request.N = nil
	}
	return d.compatible.ChatCompletions(ctx, request)
}

func (d DeepSeek) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	if err := d.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if request.N != nil {
		request.N = nil
	}
	return d.compatible.StreamChatCompletions(ctx, request, write)
}

func (d DeepSeek) ValidateResponseParameters(request openai.ResponseRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "deepseek", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	_, verbositySupplied := openai.ResponseTextVerbosity(request.Text)
	if err := validateDeepSeekUser(request.User); err != nil {
		return err
	}
	if !validDeepSeekResponseText(request.Text) {
		return &Error{Class: FailureClientRequest, Provider: "deepseek", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "text", Err: errors.New("text must contain a supported output format")}
	}
	for _, tool := range request.Tools {
		if tool.Type != "function" {
			return unsupportedDeepSeekParameter("tools.type")
		}
	}
	if reasoning := request.Reasoning; reasoning != nil {
		if reasoning.Summary != nil || reasoning.GenerateSummary != nil || reasoning.Context != nil || reasoning.Mode != nil {
			return unsupportedDeepSeekParameter("reasoning")
		}
		if reasoning.Effort != nil {
			switch *reasoning.Effort {
			case "low", "medium", "high", "xhigh", "max":
			default:
				return &Error{Class: FailureClientRequest, Provider: "deepseek", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "reasoning.effort", Err: errors.New("reasoning effort must be low, medium, high, xhigh, or max")}
			}
		}
	}
	return rejectParameters("deepseek",
		parameterCheck{"context_management", len(request.ContextManagement) > 0},
		parameterCheck{"moderation", request.Moderation != nil},
		parameterCheck{"metadata", request.Metadata != nil}, parameterCheck{"include", request.Include != nil},
		parameterCheck{"store", request.Store != nil}, parameterCheck{"truncation", request.Truncation != nil},
		parameterCheck{"safety_identifier", request.SafetyIdentifier != ""}, parameterCheck{"prompt_cache_key", request.PromptCacheKey != ""},
		parameterCheck{"prompt_cache_options", request.PromptCacheOptions != nil}, parameterCheck{"prompt_cache_retention", request.PromptCacheRetention != ""},
		parameterCheck{"stream_options", request.StreamOptions != nil},
		parameterCheck{"service_tier", request.ServiceTier != ""}, parameterCheck{"previous_response_id", request.PreviousResponse != ""},
		parameterCheck{"parallel_tool_calls", request.ParallelToolCalls != nil}, parameterCheck{"text.verbosity", verbositySupplied},
		parameterCheck{"frequency_penalty", request.FrequencyPenalty != nil}, parameterCheck{"presence_penalty", request.PresencePenalty != nil},
		parameterCheck{"max_tool_calls", request.MaxToolCalls != nil},
	)
}

func (d DeepSeek) Responses(ctx context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	if err := d.ValidateResponseParameters(request); err != nil {
		return openai.ResponseResponse{}, err
	}
	return d.compatible.Responses(ctx, request)
}

func (d DeepSeek) StreamResponses(ctx context.Context, request openai.ResponseRequest, write ResponseStreamWriter) (openai.ResponseResponse, error) {
	if err := d.ValidateResponseParameters(request); err != nil {
		return openai.ResponseResponse{}, err
	}
	return d.compatible.StreamResponses(ctx, request, write)
}

func validateDeepSeekStop(value any) error {
	var sequences []string
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		sequences = []string{typed}
	case []string:
		sequences = typed
	case []any:
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return &Error{Class: FailureClientRequest, Provider: "deepseek", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "stop", Err: errors.New("stop must be a string or up to 16 non-empty strings")}
			}
			sequences = append(sequences, text)
		}
	default:
		return &Error{Class: FailureClientRequest, Provider: "deepseek", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "stop", Err: errors.New("stop must be a string or up to 16 non-empty strings")}
	}
	if len(sequences) == 0 || len(sequences) > 16 {
		return &Error{Class: FailureClientRequest, Provider: "deepseek", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "stop", Err: errors.New("stop must be a string or up to 16 non-empty strings")}
	}
	for _, sequence := range sequences {
		if sequence == "" {
			return &Error{Class: FailureClientRequest, Provider: "deepseek", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "stop", Err: errors.New("stop must be a string or up to 16 non-empty strings")}
		}
	}
	return nil
}

func validateDeepSeekUser(value string) error {
	if value == "" {
		return nil
	}
	if utf8.RuneCountInString(value) > 512 {
		return &Error{Class: FailureClientRequest, Provider: "deepseek", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "user", Err: errors.New("user must contain at most 512 allowed characters")}
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' {
			continue
		}
		return &Error{Class: FailureClientRequest, Provider: "deepseek", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "user", Err: errors.New("user may contain only letters, digits, hyphens, and underscores")}
	}
	return nil
}

func validDeepSeekResponseText(value any) bool {
	if value == nil {
		return true
	}
	config, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for key := range config {
		if key != "format" && key != "verbosity" {
			return false
		}
	}
	format, supplied := config["format"]
	if !supplied || format == nil {
		return true
	}
	object, ok := format.(map[string]any)
	if !ok {
		return false
	}
	typeName, ok := object["type"].(string)
	if !ok {
		return false
	}
	switch typeName {
	case "text", "json_object":
		return len(object) == 1
	case "json_schema":
		name, nameOK := object["name"].(string)
		_, schemaOK := object["schema"]
		for key := range object {
			if key != "type" && key != "name" && key != "schema" && key != "description" && key != "strict" {
				return false
			}
		}
		return nameOK && name != "" && schemaOK
	default:
		return false
	}
}

func unsupportedDeepSeekParameter(name string) error {
	return &Error{Class: FailureClientRequest, Provider: "deepseek", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: name, Err: fmt.Errorf("parameter %s is not supported by this adapter", name)}
}
