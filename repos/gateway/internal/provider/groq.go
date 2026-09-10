package provider

import (
	"context"
	"net/http"

	"ai-gateway-gateway/internal/openai"
)

type Groq struct {
	compatible OpenAICompatible
}

func NewGroq(baseURL, apiKey string, stream bool) Groq {
	compatible := NewOpenAICompatible(baseURL, apiKey, stream)
	compatible.errorProvider = "groq"
	return Groq{compatible: compatible}
}

func (Groq) SupportsResponses() bool        { return false }
func (Groq) SupportsTools() bool            { return true }
func (Groq) SupportsStructuredOutput() bool { return true }
func (Groq) SupportsVision() bool           { return true }

func (g Groq) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if err := rejectParameters("groq",
		parameterCheck{"metadata", request.Metadata != nil},
		parameterCheck{"modalities", request.Modalities != nil}, parameterCheck{"audio", request.Audio != nil},
		parameterCheck{"safe_prompt", request.SafePrompt != nil},
		parameterCheck{"n", request.N != nil && *request.N != 1},
		parameterCheck{"safety_identifier", request.SafetyIdentifier != ""},
		parameterCheck{"prompt_cache_key", request.PromptCacheKey != ""}, parameterCheck{"prompt_cache_options", request.PromptCacheOptions != nil},
		parameterCheck{"prompt_cache_retention", request.PromptCacheRetention != ""}, parameterCheck{"prompt_mode", request.PromptMode != ""},
		parameterCheck{"prediction", request.Prediction != nil}, parameterCheck{"user", request.User != ""}, parameterCheck{"verbosity", request.Verbosity != ""},
		parameterCheck{"web_search_options", request.WebSearchOptions != nil}, parameterCheck{"web_fetch_options", request.WebFetchOptions != nil},
		parameterCheck{"logprobs", request.Logprobs != nil}, parameterCheck{"top_logprobs", request.TopLogprobs != nil},
		parameterCheck{"frequency_penalty", request.FrequencyPenalty != nil}, parameterCheck{"presence_penalty", request.PresencePenalty != nil},
		parameterCheck{"logit_bias", request.LogitBias != nil},
	); err != nil {
		return err
	}
	switch request.ServiceTier {
	case "", "auto", "default", "flex":
	default:
		return &Error{Class: FailureClientRequest, Provider: "groq", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: "service_tier", Err: errUnsupportedServiceTier}
	}
	return g.compatible.ValidateChatParameters(request)
}

func (g Groq) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if err := g.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return g.compatible.ChatCompletions(ctx, request)
}

func (g Groq) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	if err := g.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return g.compatible.StreamChatCompletions(ctx, request, write)
}

func (Groq) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, rejectParameters("groq", parameterCheck{"responses", true})
}
