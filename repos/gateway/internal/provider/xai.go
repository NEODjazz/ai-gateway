package provider

import (
	"context"
	"errors"
	"net/http"

	"ai-gateway-gateway/internal/openai"
)

// XAI exposes the provider operations whose wire contracts are validated here.
type XAI struct {
	compatible OpenAICompatible
}

func NewXAI(baseURL, apiKey string, stream bool) XAI {
	compatible := NewOpenAICompatible(baseURL, apiKey, stream)
	compatible.errorProvider = "xai"
	return XAI{compatible: compatible}
}

func (XAI) SupportsResponses() bool        { return true }
func (XAI) SupportsTools() bool            { return true }
func (XAI) SupportsStructuredOutput() bool { return true }
func (XAI) SupportsVision() bool           { return true }
func (XAI) SupportsWebSearch() bool        { return true }

func (x XAI) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if !validXAIServiceTier(request.ServiceTier) {
		return xaiParameterError("service_tier", "service_tier must be default or priority")
	}
	if !validXAIReasoningEffort(request.ReasoningEffort) {
		return xaiParameterError("reasoning_effort", "reasoning_effort must be none, low, medium, high, or xhigh")
	}
	if request.TopLogprobs != nil && (*request.TopLogprobs < 0 || *request.TopLogprobs > 8 || request.Logprobs == nil || !*request.Logprobs) {
		return xaiParameterError("top_logprobs", "top_logprobs must be between 0 and 8 and requires logprobs=true")
	}
	if err := rejectParameters("xai",
		parameterCheck{"metadata", request.Metadata != nil},
		parameterCheck{"store", request.Store != nil},
		parameterCheck{"modalities", request.Modalities != nil},
		parameterCheck{"audio", request.Audio != nil},
		parameterCheck{"safe_prompt", request.SafePrompt != nil},
		parameterCheck{"safety_identifier", request.SafetyIdentifier != ""},
		parameterCheck{"prompt_cache_options", request.PromptCacheOptions != nil},
		parameterCheck{"prompt_cache_retention", request.PromptCacheRetention != ""},
		parameterCheck{"prompt_mode", request.PromptMode != ""},
		parameterCheck{"prediction", request.Prediction != nil},
		parameterCheck{"verbosity", request.Verbosity != ""},
		parameterCheck{"web_fetch_options", request.WebFetchOptions != nil},
		parameterCheck{"min_p", request.MinP != nil},
		parameterCheck{"top_k", request.TopK != nil},
		parameterCheck{"top_a", request.TopA != nil},
		parameterCheck{"repetition_penalty", request.RepetitionPenalty != nil},
		parameterCheck{"logit_bias", request.LogitBias != nil},
	); err != nil {
		return err
	}
	return x.compatible.ValidateChatParameters(request)
}

func (x XAI) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if err := x.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return x.compatible.ChatCompletions(ctx, request)
}

func (x XAI) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	if err := x.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return x.compatible.StreamChatCompletions(ctx, request, write)
}

func (x XAI) ValidateResponseParameters(request openai.ResponseRequest) error {
	if request.Background {
		return xaiUnsupportedParameter("background")
	}
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "xai", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	if !validXAIServiceTier(request.ServiceTier) {
		return xaiParameterError("service_tier", "service_tier must be default or priority")
	}
	if request.TopLogprobs != nil {
		return xaiUnsupportedParameter("top_logprobs")
	}
	if request.FrequencyPenalty != nil {
		return xaiUnsupportedParameter("frequency_penalty")
	}
	if request.PresencePenalty != nil {
		return xaiUnsupportedParameter("presence_penalty")
	}
	if len(request.Metadata) > 0 {
		return xaiUnsupportedParameter("metadata")
	}
	if request.Truncation != nil {
		return xaiUnsupportedParameter("truncation")
	}
	if request.SafetyIdentifier != "" {
		return xaiUnsupportedParameter("safety_identifier")
	}
	if request.MaxToolCalls != nil {
		return xaiUnsupportedParameter("max_tool_calls")
	}
	if _, supplied := openai.ResponseTextVerbosity(request.Text); supplied {
		return xaiUnsupportedParameter("text.verbosity")
	}
	if reasoning := request.Reasoning; reasoning != nil {
		if reasoning.Summary != nil || reasoning.GenerateSummary != nil || reasoning.Context != nil || reasoning.Mode != nil {
			return xaiUnsupportedParameter("reasoning")
		}
		if reasoning.Effort != nil && !validXAIReasoningEffort(*reasoning.Effort) {
			return xaiParameterError("reasoning.effort", "reasoning effort must be low, medium, high, or xhigh")
		}
	}
	return x.compatible.ValidateResponseParameters(request)
}

func (x XAI) Responses(ctx context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	if err := x.ValidateResponseParameters(request); err != nil {
		return openai.ResponseResponse{}, err
	}
	return x.compatible.Responses(ctx, request)
}

func (x XAI) StreamResponses(ctx context.Context, request openai.ResponseRequest, write ResponseStreamWriter) (openai.ResponseResponse, error) {
	if err := x.ValidateResponseParameters(request); err != nil {
		return openai.ResponseResponse{}, err
	}
	return x.compatible.StreamResponses(ctx, request, write)
}

func (x XAI) RetrieveResponse(ctx context.Context, id string) (openai.ResponseResponse, error) {
	return x.compatible.RetrieveResponse(ctx, id)
}

func (x XAI) ListResponseInputItems(ctx context.Context, id string, options ResponseInputItemsOptions) (openai.ResponseInputItemList, error) {
	return x.compatible.ListResponseInputItems(ctx, id, options)
}

func (x XAI) DeleteResponse(ctx context.Context, id string) (openai.ResponseDeletion, error) {
	return x.compatible.deleteResponse(ctx, id, "response")
}

func (x XAI) CompactResponse(ctx context.Context, request openai.ResponseCompactRequest) (openai.CompactedResponse, error) {
	return x.compatible.CompactResponse(ctx, request)
}

func validXAIServiceTier(value string) bool {
	return value == "" || value == "default" || value == "priority"
}

func validXAIReasoningEffort(value string) bool {
	return value == "" || value == "none" || value == "low" || value == "medium" || value == "high" || value == "xhigh"
}

func xaiParameterError(param, message string) error {
	return &Error{Class: FailureClientRequest, Provider: "xai", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: param, Err: errors.New(message)}
}

func xaiUnsupportedParameter(param string) error {
	return &Error{Class: FailureClientRequest, Provider: "xai", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: param, Err: errors.New(param + " is not supported by this adapter")}
}
