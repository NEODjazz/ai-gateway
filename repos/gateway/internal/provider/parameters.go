package provider

import (
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
	if err := rejectToolCallMetadata("anthropic", request.Messages); err != nil {
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
	return rejectParameters("anthropic",
		parameterCheck{"input", hasOpaqueResponseContext(request.Input)},
		parameterCheck{"include", len(request.Include) > 0}, parameterCheck{"store", request.Store != nil},
		parameterCheck{"previous_response_id", request.PreviousResponse != ""},
	)
}

func (Ollama) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if err := rejectToolCallMetadata("ollama", request.Messages); err != nil {
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
	return rejectParameters("ollama", parameterCheck{"user", request.User != ""}, parameterCheck{"encoding_format", request.EncodingFormat != "" && request.EncodingFormat != "float"})
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

func rejectGenerationOptions(adapter string, options openai.ChatGenerationOptions) error {
	return rejectParameters(adapter,
		parameterCheck{"reasoning_effort", options.ReasoningEffort != ""},
		parameterCheck{"logprobs", options.Logprobs != nil},
		parameterCheck{"top_logprobs", options.TopLogprobs != nil},
		parameterCheck{"frequency_penalty", options.FrequencyPenalty != nil},
		parameterCheck{"presence_penalty", options.PresencePenalty != nil},
		parameterCheck{"logit_bias", options.LogitBias != nil},
	)
}

func (Demo) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if err := rejectToolCallMetadata("demo", request.Messages); err != nil {
		return err
	}
	return rejectGenerationOptions("demo", request.ChatGenerationOptions)
}

func (OpenAICompatible) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if message := request.ChatGenerationOptions.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "openai-compatible", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: fmt.Errorf("%s", message)}
	}
	return nil
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

func (Demo) ValidateResponseParameters(request openai.ResponseRequest) error {
	return rejectParameters("demo", parameterCheck{"include", len(request.Include) > 0}, parameterCheck{"store", request.Store != nil})
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
