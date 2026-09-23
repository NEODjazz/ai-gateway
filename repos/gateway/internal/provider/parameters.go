package provider

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"unicode/utf8"

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

func rejectChatModeration(adapter string, request openai.ChatCompletionRequest) error {
	return rejectParameters(adapter, parameterCheck{"moderation", request.Moderation != nil})
}

var errUnsupportedServiceTier = errors.New("service_tier is not supported by this adapter")

func (Anthropic) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if err := rejectChatModeration("anthropic", request); err != nil {
		return err
	}
	if err := validateChatReasoningContent("anthropic", request.Messages, false); err != nil {
		return err
	}
	if err := validateChatMessagePrefix("anthropic", request.Messages, true); err != nil {
		return err
	}
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
	options := request.ChatGenerationOptions
	switch options.ServiceTier {
	case "", "auto", "standard_only":
		options.ServiceTier = ""
	default:
		return rejectParameters("anthropic", parameterCheck{"service_tier", true})
	}
	if options.Metadata != nil {
		if len(options.Metadata) != 1 || options.Metadata["user_id"] == "" || utf8.RuneCountInString(options.Metadata["user_id"]) > 512 {
			return &Error{Class: FailureClientRequest, Provider: "anthropic", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "metadata", Err: errors.New("metadata requires one non-empty user_id of at most 512 characters")}
		}
		options.Metadata = nil
	}
	switch options.ReasoningEffort {
	case "", "low", "medium", "high", "xhigh", "max":
		options.ReasoningEffort = ""
	default:
		return &Error{Class: FailureClientRequest, Provider: "anthropic", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "reasoning_effort", Err: errors.New("reasoning_effort must be low, medium, high, xhigh, or max")}
	}
	if options.WebSearchOptions != nil {
		if options.WebSearchOptions.SearchContextSize != "" {
			return &Error{Class: FailureClientRequest, Provider: "anthropic", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: "web_search_options.search_context_size", Err: errors.New("search_context_size cannot be represented by this adapter")}
		}
		options.WebSearchOptions = nil
	}
	options.WebFetchOptions = nil
	if options.TopK != nil {
		if *options.TopK < 0 {
			return &Error{Class: FailureClientRequest, Provider: "anthropic", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "top_k", Err: errors.New("top_k must be non-negative")}
		}
		options.TopK = nil
	}
	if err := rejectGenerationOptions("anthropic", options); err != nil {
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
	for _, tool := range request.Tools {
		if tool.Type != "function" {
			return &Error{Class: FailureClientRequest, Provider: "anthropic", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: "tools", Err: fmt.Errorf("Responses tool type %q is not supported by this adapter", tool.Type)}
		}
	}
	_, verbositySupplied := openai.ResponseTextVerbosity(request.Text)
	return rejectParameters("anthropic",
		parameterCheck{"background", request.Background},
		parameterCheck{"context_management", len(request.ContextManagement) > 0},
		parameterCheck{"moderation", request.Moderation != nil},
		parameterCheck{"input", hasOpaqueResponseContext(request.Input)},
		parameterCheck{"include", len(request.Include) > 0}, parameterCheck{"store", request.Store != nil}, parameterCheck{"reasoning", request.Reasoning != nil}, parameterCheck{"metadata", len(request.Metadata) > 0}, parameterCheck{"truncation", request.Truncation != nil}, parameterCheck{"top_logprobs", request.TopLogprobs != nil},
		parameterCheck{"safety_identifier", request.SafetyIdentifier != ""},
		parameterCheck{"user", request.User != ""},
		parameterCheck{"prompt_cache_key", request.PromptCacheKey != ""},
		parameterCheck{"prompt_cache_options", request.PromptCacheOptions != nil},
		parameterCheck{"prompt_cache_retention", request.PromptCacheRetention != ""},
		parameterCheck{"stream_options", request.StreamOptions != nil},
		parameterCheck{"text.verbosity", verbositySupplied},
		parameterCheck{"service_tier", request.ServiceTier != ""},
		parameterCheck{"previous_response_id", request.PreviousResponse != ""},
		parameterCheck{"frequency_penalty", request.FrequencyPenalty != nil},
		parameterCheck{"presence_penalty", request.PresencePenalty != nil},
		parameterCheck{"max_tool_calls", request.MaxToolCalls != nil},
	)
}

func (Ollama) ValidateResponseParameters(request openai.ResponseRequest) error {
	_, verbositySupplied := openai.ResponseTextVerbosity(request.Text)
	return rejectParameters("ollama", parameterCheck{"background", request.Background}, parameterCheck{"context_management", len(request.ContextManagement) > 0}, parameterCheck{"moderation", request.Moderation != nil}, parameterCheck{"user", request.User != ""}, parameterCheck{"safety_identifier", request.SafetyIdentifier != ""}, parameterCheck{"prompt_cache_key", request.PromptCacheKey != ""}, parameterCheck{"prompt_cache_options", request.PromptCacheOptions != nil}, parameterCheck{"prompt_cache_retention", request.PromptCacheRetention != ""}, parameterCheck{"stream_options", request.StreamOptions != nil}, parameterCheck{"text.verbosity", verbositySupplied}, parameterCheck{"service_tier", request.ServiceTier != ""}, parameterCheck{"frequency_penalty", request.FrequencyPenalty != nil}, parameterCheck{"presence_penalty", request.PresencePenalty != nil}, parameterCheck{"max_tool_calls", request.MaxToolCalls != nil})
}

func (Ollama) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if err := rejectChatModeration("ollama", request); err != nil {
		return err
	}
	if err := validateChatReasoningContent("ollama", request.Messages, true); err != nil {
		return err
	}
	if err := validateChatMessagePrefix("ollama", request.Messages, false); err != nil {
		return err
	}
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
	options := request.ChatGenerationOptions
	if options.TopK != nil && (*options.TopK < 0 || *options.TopK > 1_000_000) {
		return &Error{Class: FailureClientRequest, Provider: "ollama", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_parameter", Param: "top_k", Err: fmt.Errorf("parameter top_k must be between 0 and 1000000")}
	}
	if options.MinP != nil && (math.IsNaN(*options.MinP) || math.IsInf(*options.MinP, 0) || *options.MinP < 0 || *options.MinP > 1) {
		return &Error{Class: FailureClientRequest, Provider: "ollama", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_parameter", Param: "min_p", Err: fmt.Errorf("parameter min_p must be a finite number between 0 and 1")}
	}
	if options.TopLogprobs != nil && (*options.TopLogprobs < 0 || *options.TopLogprobs > 20 || options.Logprobs == nil || !*options.Logprobs) {
		return &Error{Class: FailureClientRequest, Provider: "ollama", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_parameter", Param: "top_logprobs", Err: fmt.Errorf("parameter top_logprobs must be between 0 and 20 and requires logprobs=true")}
	}
	options.TopK = nil
	options.MinP = nil
	options.Logprobs = nil
	options.TopLogprobs = nil
	switch options.ReasoningEffort {
	case "none", "low", "medium", "high", "max":
		options.ReasoningEffort = ""
	}
	if err := rejectGenerationOptions("ollama", options); err != nil {
		return err
	}
	return rejectParameters("ollama",
		parameterCheck{"tool_choice", request.ToolChoice != nil},
		parameterCheck{"parallel_tool_calls", request.ParallelToolCalls != nil},
	)
}

func (Ollama) ValidateEmbeddingParameters(request openai.EmbeddingRequest) error {
	input, err := openai.InspectEmbeddingInput(request.Input)
	return rejectParameters("ollama", parameterCheck{"metadata", request.Metadata != nil}, parameterCheck{"output_dtype", request.OutputDType != ""}, parameterCheck{"input", err != nil || input.Tokenized()}, parameterCheck{"input_type", request.InputType != ""}, parameterCheck{"user", request.User != ""}, parameterCheck{"encoding_format", request.EncodingFormat != "" && request.EncodingFormat != "float"})
}

func (Demo) ValidateEmbeddingParameters(request openai.EmbeddingRequest) error {
	input, err := openai.InspectEmbeddingInput(request.Input)
	return rejectParameters("demo", parameterCheck{"metadata", request.Metadata != nil}, parameterCheck{"output_dtype", request.OutputDType != ""}, parameterCheck{"input", err != nil || input.Tokenized()}, parameterCheck{"input_type", request.InputType != ""}, parameterCheck{"encoding_format", request.EncodingFormat != "" && request.EncodingFormat != "float"}, parameterCheck{"user", request.User != ""})
}

func (p OpenAICompatible) ValidateEmbeddingParameters(request openai.EmbeddingRequest) error {
	return rejectParameters(p.providerName(), parameterCheck{"metadata", request.Metadata != nil && p.providerName() != "mistral"}, parameterCheck{"output_dtype", request.OutputDType != "" && p.providerName() != "mistral"}, parameterCheck{"input_type", request.InputType != ""})
}

func validateChatAdapter(client Client, request openai.ChatCompletionRequest) error {
	if request.BedrockServiceTier != "" || request.BedrockPerformanceLatency != "" || len(request.BedrockAdditionalModelRequestFields) > 0 || len(request.BedrockAdditionalModelResponseFieldPaths) > 0 || len(request.BedrockRequestMetadata) > 0 || request.BedrockGuardrailConfig != nil {
		support, ok := client.(interface{ SupportsBedrockNativeControls() bool })
		if !ok || !support.SupportsBedrockNativeControls() {
			return rejectParameters("provider", parameterCheck{"bedrock_native_controls", true})
		}
	}
	for _, message := range request.Messages {
		if len(message.Reasoning) == 0 {
			continue
		}
		if message.Role != "assistant" {
			return &Error{Class: FailureClientRequest, Provider: "provider", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "messages.reasoning", Err: errors.New("reasoning blocks require an assistant message")}
		}
		validateReasoning := openai.ValidateReasoningBlocks
		if support, ok := client.(interface{ SupportsUnsignedReasoning() bool }); ok && support.SupportsUnsignedReasoning() {
			validateReasoning = openai.ValidateBedrockReasoningBlocks
		}
		if err := validateReasoning(message.Reasoning); err != nil {
			return &Error{Class: FailureClientRequest, Provider: "provider", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "messages.reasoning", Err: err}
		}
		support, ok := client.(interface{ SupportsReasoningBlocks() bool })
		if !ok || !support.SupportsReasoningBlocks() {
			return rejectParameters("provider", parameterCheck{"messages.reasoning", true})
		}
	}
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

func validateRerankAdapter(client Client, request openai.RerankRequest) error {
	if validator, ok := client.(interface {
		ValidateRerankParameters(openai.RerankRequest) error
	}); ok {
		return validator.ValidateRerankParameters(request)
	}
	return nil
}

func validateModerationAdapter(client ModerationClient, request openai.ModerationRequest) error {
	if validator, ok := client.(interface {
		ValidateModerationParameters(openai.ModerationRequest) error
	}); ok {
		return validator.ValidateModerationParameters(request)
	}
	return nil
}

func (p OpenAICompatible) ValidateModerationParameters(request openai.ModerationRequest) error {
	return rejectParameters(p.providerName(), parameterCheck{"metadata", request.Metadata != nil && p.providerName() != "mistral"})
}

func validateCompletionAdapter(client CompletionClient, request openai.CompletionRequest) error {
	if validator, ok := client.(interface {
		ValidateCompletionParameters(openai.CompletionRequest) error
	}); ok {
		return validator.ValidateCompletionParameters(request)
	}
	return nil
}

func (p OpenAICompatible) ValidateCompletionParameters(request openai.CompletionRequest) error {
	return rejectParameters(p.providerName(),
		parameterCheck{"metadata", request.Metadata != nil},
		parameterCheck{"min_tokens", request.MinTokens != nil},
		parameterCheck{"prompt_cache_key", request.PromptCacheKey != ""},
	)
}

func (Ollama) ValidateCompletionParameters(request openai.CompletionRequest) error {
	prompt, err := openai.InspectCompletionPrompt(request.Prompt)
	if err != nil {
		return &Error{Class: FailureClientRequest, Provider: "ollama", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "prompt", Err: err}
	}
	return rejectParameters("ollama",
		parameterCheck{"prompt", prompt.Kind != openai.CompletionPromptText},
		parameterCheck{"metadata", request.Metadata != nil},
		parameterCheck{"min_tokens", request.MinTokens != nil},
		parameterCheck{"prompt_cache_key", request.PromptCacheKey != ""},
	)
}

func rejectGenerationOptions(adapter string, options openai.ChatGenerationOptions) error {
	return rejectParameters(adapter,
		parameterCheck{"metadata", options.Metadata != nil},
		parameterCheck{"store", options.Store != nil},
		parameterCheck{"modalities", options.Modalities != nil},
		parameterCheck{"audio", options.Audio != nil},
		parameterCheck{"moderation", options.Moderation != nil},
		parameterCheck{"clear_thinking", options.ClearThinking != nil},
		parameterCheck{"citation_options", options.CitationOptions != ""},
		parameterCheck{"include_reasoning", options.IncludeReasoning != nil},
		parameterCheck{"reasoning_format", options.ReasoningFormat != ""},
		parameterCheck{"reasoning_effort", options.ReasoningEffort != ""},
		parameterCheck{"safe_prompt", options.SafePrompt != nil},
		parameterCheck{"n", options.N != nil},
		parameterCheck{"safety_identifier", options.SafetyIdentifier != ""},
		parameterCheck{"prompt_cache_key", options.PromptCacheKey != ""},
		parameterCheck{"prompt_cache_options", options.PromptCacheOptions != nil},
		parameterCheck{"prompt_cache_retention", options.PromptCacheRetention != ""},
		parameterCheck{"prompt_mode", options.PromptMode != ""},
		parameterCheck{"prediction", options.Prediction != nil},
		parameterCheck{"service_tier", options.ServiceTier != ""},
		parameterCheck{"user", options.User != ""},
		parameterCheck{"verbosity", options.Verbosity != ""},
		parameterCheck{"web_search_options", options.WebSearchOptions != nil},
		parameterCheck{"web_fetch_options", options.WebFetchOptions != nil},
		parameterCheck{"logprobs", options.Logprobs != nil},
		parameterCheck{"top_logprobs", options.TopLogprobs != nil},
		parameterCheck{"frequency_penalty", options.FrequencyPenalty != nil},
		parameterCheck{"presence_penalty", options.PresencePenalty != nil},
		parameterCheck{"min_p", options.MinP != nil},
		parameterCheck{"top_k", options.TopK != nil},
		parameterCheck{"top_a", options.TopA != nil},
		parameterCheck{"repetition_penalty", options.RepetitionPenalty != nil},
		parameterCheck{"logit_bias", options.LogitBias != nil},
	)
}

func (Demo) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if err := rejectChatModeration("demo", request); err != nil {
		return err
	}
	if err := validateChatReasoningContent("demo", request.Messages, false); err != nil {
		return err
	}
	if err := validateChatMessagePrefix("demo", request.Messages, false); err != nil {
		return err
	}
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

func (p OpenAICompatible) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	providerName := p.providerName()
	if err := validateChatReasoningContent(providerName, request.Messages, true); err != nil {
		return err
	}
	if err := validateChatMessagePrefix(providerName, request.Messages, p.supportsMessagePrefix); err != nil {
		return err
	}
	if err := openai.ValidateLegacyFunctionRequest(request); err != nil {
		return &Error{Class: FailureClientRequest, Provider: providerName, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "functions", Err: err}
	}
	if err := validateChatPromptCacheBreakpoints(providerName, request, true); err != nil {
		return err
	}
	for _, message := range request.Messages {
		if message.Audio != nil {
			if message.Role != "assistant" {
				return &Error{Class: FailureClientRequest, Provider: providerName, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "messages.audio", Err: errors.New("messages.audio requires role=assistant")}
			}
			if err := openai.ValidateChatAudioReference(message.Audio); err != nil {
				return &Error{Class: FailureClientRequest, Provider: providerName, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "messages.audio", Err: err}
			}
		}
	}
	if message := request.ChatGenerationOptions.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: providerName, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: fmt.Errorf("%s", message)}
	}
	return rejectParameters(providerName,
		parameterCheck{"store", request.Store != nil && *request.Store},
		parameterCheck{"clear_thinking", request.ClearThinking != nil && providerName != "cerebras"},
		parameterCheck{"citation_options", request.CitationOptions != "" && providerName != "groq"},
		parameterCheck{"include_reasoning", request.IncludeReasoning != nil && providerName != "groq"},
		parameterCheck{"reasoning_format", request.ReasoningFormat != "" && providerName != "groq"},
		parameterCheck{"safe_prompt", request.SafePrompt != nil && !p.supportsSafePrompt},
		parameterCheck{"prompt_mode", request.PromptMode != "" && !p.supportsPromptMode},
		parameterCheck{"service_tier", !supportedCompatibleServiceTier(providerName, request.ServiceTier)},
	)
}

func supportedCompatibleServiceTier(providerName, value string) bool {
	if value == "" {
		return true
	}
	switch providerName {
	case "openai":
		return value == "auto" || value == "default" || value == "flex" || value == "priority" || value == "fast" || value == "ultrafast"
	case "xai":
		return value == "default" || value == "priority"
	case "cerebras":
		return value == "auto" || value == "default" || value == "flex" || value == "priority"
	case "groq", "openrouter":
		return true
	default:
		return false
	}
}

func validateChatReasoningContent(adapter string, messages []openai.Message, supported bool) error {
	for _, message := range messages {
		if err := openai.ValidateChatReasoningContent(message.Role, message.ReasoningContent); err != nil {
			return &Error{Class: FailureClientRequest, Provider: adapter, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "messages.reasoning_content", Err: err}
		}
		if message.ReasoningContent != "" && !supported {
			return rejectParameters(adapter, parameterCheck{"messages.reasoning_content", true})
		}
	}
	return nil
}

func validateChatMessagePrefix(adapter string, messages []openai.Message, supported bool) error {
	for index, message := range messages {
		if message.Prefix == nil {
			continue
		}
		if !supported {
			return rejectParameters(adapter, parameterCheck{"messages.prefix", true})
		}
		if message.Role != "assistant" || (*message.Prefix && (index != len(messages)-1 || openai.ContentText(message.Content) == "")) {
			return &Error{Class: FailureClientRequest, Provider: adapter, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "messages.prefix", Err: errors.New("prefix requires an assistant message and prefix=true requires the final message with non-empty text")}
		}
	}
	return nil
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

func (p OpenAICompatible) ValidateResponseParameters(request openai.ResponseRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: p.providerName(), StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: fmt.Errorf("%s", message)}
	}
	providerName := p.providerName()
	return rejectParameters(providerName, parameterCheck{"service_tier", !supportedCompatibleServiceTier(providerName, request.ServiceTier)})
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
	return rejectParameters("demo",
		parameterCheck{"background", request.Background}, parameterCheck{"context_management", len(request.ContextManagement) > 0}, parameterCheck{"moderation", request.Moderation != nil}, parameterCheck{"include", len(request.Include) > 0}, parameterCheck{"store", request.Store != nil},
		parameterCheck{"reasoning", request.Reasoning != nil}, parameterCheck{"metadata", len(request.Metadata) > 0}, parameterCheck{"truncation", request.Truncation != nil},
		parameterCheck{"top_logprobs", request.TopLogprobs != nil}, parameterCheck{"instructions", request.Instructions != ""}, parameterCheck{"tools", len(request.Tools) > 0},
		parameterCheck{"tool_choice", request.ToolChoice != nil}, parameterCheck{"parallel_tool_calls", request.ParallelToolCalls != nil}, parameterCheck{"text", request.Text != nil && !verbositySupplied},
		parameterCheck{"previous_response_id", request.PreviousResponse != ""}, parameterCheck{"user", request.User != ""}, parameterCheck{"safety_identifier", request.SafetyIdentifier != ""},
		parameterCheck{"prompt_cache_key", request.PromptCacheKey != ""}, parameterCheck{"text.verbosity", verbositySupplied}, parameterCheck{"service_tier", request.ServiceTier != ""},
		parameterCheck{"prompt_cache_options", request.PromptCacheOptions != nil}, parameterCheck{"prompt_cache_retention", request.PromptCacheRetention != ""},
		parameterCheck{"stream_options", request.StreamOptions != nil},
		parameterCheck{"max_output_tokens", request.MaxOutputTokens != nil}, parameterCheck{"max_tokens", request.MaxTokens != nil}, parameterCheck{"temperature", request.Temperature != nil},
		parameterCheck{"top_p", request.TopP != nil}, parameterCheck{"frequency_penalty", request.FrequencyPenalty != nil}, parameterCheck{"presence_penalty", request.PresencePenalty != nil},
		parameterCheck{"max_tool_calls", request.MaxToolCalls != nil},
	)
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
