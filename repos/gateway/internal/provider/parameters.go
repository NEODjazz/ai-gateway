package provider

import (
	"errors"
	"fmt"
	"net/http"

	"ai-gateway-gateway/internal/openai"
)

// parameterCheck describes a supplied field that an adapter cannot represent.
// Rejection is terminal: retry or fallback must not discard request semantics.
type parameterCheck struct {
	name     string
	supplied bool
}

func rejectParameters(adapter string, checks ...parameterCheck) error {
	for _, check := range checks {
		if check.supplied {
			return &Error{Class: FailureClientRequest, Provider: adapter, StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: check.name, Err: fmt.Errorf("parameter %s is not supported by this adapter", check.name)}
		}
	}
	return nil
}

func (Anthropic) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if err := rejectLegacyFunctionCalling("anthropic", request); err != nil {
		return err
	}
	if err := validateChatPromptCacheBreakpoints("anthropic", request, true); err != nil {
		return err
	}
	if request.ToolChoice == "none" {
		for _, tool := range request.Tools {
			if tool.Function.PromptCacheBreakpoint != nil {
				return &Error{Class: FailureClientRequest, Provider: "anthropic", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "tool_choice", Err: errors.New("tool_choice=none cannot be combined with a tool prompt_cache_breakpoint")}
			}
		}
	}
	if err := rejectToolCallMetadata("anthropic", request.Messages); err != nil {
		return err
	}
	if err := rejectChatMessageRefusals("anthropic", request.Messages); err != nil {
		return err
	}
	if err := rejectChatMessageAudio("anthropic", request.Messages); err != nil {
		return err
	}
	if err := rejectGenerationOptions("anthropic", request.ChatGenerationOptions); err != nil {
		return err
	}
	_, validStop := openai.StopSequences(request.Stop)
	return rejectParameters("anthropic",
		parameterCheck{"stop", !validStop},
		parameterCheck{"seed", request.Seed != nil},
	)
}

func (Anthropic) ValidateResponseParameters(request openai.ResponseRequest) error {
	if err := validateAnthropicResponseHistory(request.Input); err != nil {
		return err
	}
	_, verbositySupplied := openai.ResponseTextVerbosity(request.Text)
	return rejectParameters("anthropic",
		parameterCheck{"input", hasOpaqueResponseContext(request.Input)},
		parameterCheck{"include", len(request.Include) > 0}, parameterCheck{"store", request.Store != nil}, parameterCheck{"reasoning", request.Reasoning != nil}, parameterCheck{"metadata", len(request.Metadata) > 0}, parameterCheck{"truncation", request.Truncation != nil}, parameterCheck{"top_logprobs", request.TopLogprobs != nil},
		parameterCheck{"safety_identifier", request.SafetyIdentifier != ""},
		parameterCheck{"prompt_cache_key", request.PromptCacheKey != ""},
		parameterCheck{"text.verbosity", verbositySupplied},
		parameterCheck{"service_tier", request.ServiceTier != ""},
		parameterCheck{"previous_response_id", request.PreviousResponse != ""},
	)
}

func (Ollama) ValidateResponseParameters(request openai.ResponseRequest) error {
	_, verbositySupplied := openai.ResponseTextVerbosity(request.Text)
	return rejectParameters("ollama", parameterCheck{"safety_identifier", request.SafetyIdentifier != ""}, parameterCheck{"prompt_cache_key", request.PromptCacheKey != ""}, parameterCheck{"text.verbosity", verbositySupplied}, parameterCheck{"service_tier", request.ServiceTier != ""})
}

func (Ollama) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if err := rejectLegacyFunctionCalling("ollama", request); err != nil {
		return err
	}
	if err := validateChatPromptCacheBreakpoints("ollama", request, false); err != nil {
		return err
	}
	if err := rejectToolCallMetadata("ollama", request.Messages); err != nil {
		return err
	}
	if err := rejectChatMessageRefusals("ollama", request.Messages); err != nil {
		return err
	}
	if err := rejectChatMessageAudio("ollama", request.Messages); err != nil {
		return err
	}
	if err := rejectGenerationOptions("ollama", request.ChatGenerationOptions); err != nil {
		return err
	}
	return rejectParameters("ollama",
		parameterCheck{"tool_choice", request.ToolChoice != nil},
		parameterCheck{"parallel_tool_calls", request.ParallelToolCalls != nil},
	)
}

func (Ollama) ValidateEmbeddingParameters(request openai.EmbeddingRequest) error {
	input, err := openai.InspectEmbeddingInput(request.Input)
	return rejectParameters("ollama", parameterCheck{"input", err != nil || input.Tokenized()}, parameterCheck{"user", request.User != ""}, parameterCheck{"encoding_format", request.EncodingFormat != "" && request.EncodingFormat != "float"})
}

func (Demo) ValidateEmbeddingParameters(request openai.EmbeddingRequest) error {
	input, err := openai.InspectEmbeddingInput(request.Input)
	return rejectParameters("demo", parameterCheck{"input", err != nil || input.Tokenized()}, parameterCheck{"encoding_format", request.EncodingFormat != "" && request.EncodingFormat != "float"})
}

func validateChatAdapter(client Client, request openai.ChatCompletionRequest) error {
	if request.RequireMatchedStop {
		reporter, ok := client.(interface{ ReportsMatchedStop() bool })
		if !ok || !reporter.ReportsMatchedStop() {
			return rejectParameters("provider", parameterCheck{"stop_sequences", true})
		}
	}
	if validator, ok := client.(interface {
		ValidateChatParameters(openai.ChatCompletionRequest) error
	}); ok {
		return validator.ValidateChatParameters(request)
	}
	return nil
}

func validateResponseAdapter(client Client, request openai.ResponseRequest) error {
	if validator, ok := client.(interface {
		ValidateResponseParameters(openai.ResponseRequest) error
	}); ok {
		return validator.ValidateResponseParameters(request)
	}
	return nil
}

func validateEmbeddingAdapter(client Client, request openai.EmbeddingRequest) error {
	if validator, ok := client.(interface {
		ValidateEmbeddingParameters(openai.EmbeddingRequest) error
	}); ok {
		return validator.ValidateEmbeddingParameters(request)
	}
	return nil
}

func validateCompletionAdapter(client CompletionClient, request openai.CompletionRequest) error {
	if validator, ok := client.(interface {
		ValidateCompletionParameters(openai.CompletionRequest) error
	}); ok {
		return validator.ValidateCompletionParameters(request)
	}
	return nil
}

func (Ollama) ValidateCompletionParameters(request openai.CompletionRequest) error {
	prompt, err := openai.InspectCompletionPrompt(request.Prompt)
	if err != nil {
		return &Error{Class: FailureClientRequest, Provider: "ollama", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "prompt", Err: err}
	}
	return rejectParameters("ollama", parameterCheck{"prompt", prompt.Kind != openai.CompletionPromptText})
}

func rejectGenerationOptions(adapter string, options openai.ChatGenerationOptions) error {
	return rejectParameters(adapter,
		parameterCheck{"metadata", options.Metadata != nil},
		parameterCheck{"store", options.Store != nil},
		parameterCheck{"modalities", options.Modalities != nil},
		parameterCheck{"audio", options.Audio != nil},
		parameterCheck{"reasoning_effort", options.ReasoningEffort != ""},
		parameterCheck{"n", options.N != nil},
		parameterCheck{"safety_identifier", options.SafetyIdentifier != ""},
		parameterCheck{"prompt_cache_key", options.PromptCacheKey != ""},
		parameterCheck{"prompt_cache_options", options.PromptCacheOptions != nil},
		parameterCheck{"prompt_cache_retention", options.PromptCacheRetention != ""},
		parameterCheck{"prediction", options.Prediction != nil},
		parameterCheck{"service_tier", options.ServiceTier != ""},
		parameterCheck{"user", options.User != ""},
		parameterCheck{"verbosity", options.Verbosity != ""},
		parameterCheck{"web_search_options", options.WebSearchOptions != nil},
		parameterCheck{"logprobs", options.Logprobs != nil},
		parameterCheck{"top_logprobs", options.TopLogprobs != nil},
		parameterCheck{"frequency_penalty", options.FrequencyPenalty != nil},
		parameterCheck{"presence_penalty", options.PresencePenalty != nil},
		parameterCheck{"logit_bias", options.LogitBias != nil},
	)
}

func (Demo) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if err := rejectLegacyFunctionCalling("demo", request); err != nil {
		return err
	}
	if err := validateChatPromptCacheBreakpoints("demo", request, false); err != nil {
		return err
	}
	if err := rejectToolCallMetadata("demo", request.Messages); err != nil {
		return err
	}
	if err := rejectChatMessageRefusals("demo", request.Messages); err != nil {
		return err
	}
	if err := rejectChatMessageAudio("demo", request.Messages); err != nil {
		return err
	}
	return rejectGenerationOptions("demo", request.ChatGenerationOptions)
}

func (OpenAICompatible) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if err := openai.ValidateLegacyFunctionRequest(request); err != nil {
		return &Error{Class: FailureClientRequest, Provider: "openai-compatible", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "functions", Err: err}
	}
	if err := validateChatPromptCacheBreakpoints("openai-compatible", request, true); err != nil {
		return err
	}
	for _, message := range request.Messages {
		if message.Audio != nil {
			if message.Role != "assistant" {
				return &Error{Class: FailureClientRequest, Provider: "openai-compatible", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "messages.audio", Err: errors.New("messages.audio requires role=assistant")}
			}
			if err := openai.ValidateChatAudioReference(message.Audio); err != nil {
				return &Error{Class: FailureClientRequest, Provider: "openai-compatible", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "messages.audio", Err: err}
			}
		}
	}
	if message := request.ChatGenerationOptions.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "openai-compatible", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: fmt.Errorf("%s", message)}
	}
	return rejectParameters("openai-compatible",
		parameterCheck{"store", request.Store != nil && *request.Store},
		parameterCheck{"service_tier", request.ServiceTier != ""},
	)
}

func rejectLegacyFunctionCalling(adapter string, request openai.ChatCompletionRequest) error {
	if err := rejectParameters(adapter,
		parameterCheck{"functions", len(request.Functions) > 0},
		parameterCheck{"function_call", request.FunctionCall != nil},
	); err != nil {
		return err
	}
	for _, message := range request.Messages {
		if message.FunctionCall != nil || message.Role == "function" {
			return rejectParameters(adapter, parameterCheck{"messages.function_call", true})
		}
	}
	return nil
}

func validateChatPromptCacheBreakpoints(adapter string, request openai.ChatCompletionRequest, supported bool) error {
	count, message := openai.ChatRequestPromptCacheBreakpoints(request)
	if message != "" {
		return &Error{Class: FailureClientRequest, Provider: adapter, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "messages.prompt_cache_breakpoint", Err: fmt.Errorf("%s", message)}
	}
	return rejectParameters(adapter, parameterCheck{"messages.prompt_cache_breakpoint", count > 0 && !supported})
}

func (OpenAICompatible) ValidateResponseParameters(request openai.ResponseRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "openai-compatible", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: fmt.Errorf("%s", message)}
	}
	return rejectParameters("openai-compatible", parameterCheck{"service_tier", request.ServiceTier != ""})
}

func rejectToolCallMetadata(adapter string, messages []openai.Message) error {
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if call.ExtraContent != nil {
				return rejectParameters(adapter, parameterCheck{"messages.tool_calls.extra_content", true})
			}
		}
	}
	return nil
}

func rejectChatMessageRefusals(adapter string, messages []openai.Message) error {
	for _, message := range messages {
		if message.Refusal != nil {
			return rejectParameters(adapter, parameterCheck{"messages.refusal", true})
		}
	}
	return nil
}

func rejectChatMessageAudio(adapter string, messages []openai.Message) error {
	for _, message := range messages {
		if message.Audio != nil {
			return rejectParameters(adapter, parameterCheck{"messages.audio", true})
		}
	}
	return nil
}

func (Demo) ValidateResponseParameters(request openai.ResponseRequest) error {
	_, verbositySupplied := openai.ResponseTextVerbosity(request.Text)
	return rejectParameters("demo", parameterCheck{"include", len(request.Include) > 0}, parameterCheck{"store", request.Store != nil}, parameterCheck{"reasoning", request.Reasoning != nil}, parameterCheck{"metadata", len(request.Metadata) > 0}, parameterCheck{"truncation", request.Truncation != nil}, parameterCheck{"top_logprobs", request.TopLogprobs != nil}, parameterCheck{"safety_identifier", request.SafetyIdentifier != ""}, parameterCheck{"prompt_cache_key", request.PromptCacheKey != ""}, parameterCheck{"text.verbosity", verbositySupplied}, parameterCheck{"service_tier", request.ServiceTier != ""})
}

// Provider-specific reasoning and compaction cannot be flattened into messages.
func hasOpaqueResponseContext(input any) bool {
	switch value := input.(type) {
	case []any:
		for _, item := range value {
			if hasOpaqueResponseContext(item) {
				return true
			}
		}
	case map[string]any:
		if value["type"] == "reasoning" || value["type"] == "compaction" {
			return true
		}
		_, present := value["encrypted_content"]
		return present
	}
	return false
}

func validateResponseOptions(request openai.ResponseRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: fmt.Errorf("%s", message)}
	}
	return nil
}

func responseOutputTokenLimit(request openai.ResponseRequest) *int {
	if request.MaxOutputTokens != nil {
		return request.MaxOutputTokens
	}
	return request.MaxTokens
}
