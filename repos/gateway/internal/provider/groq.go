package provider

import (
	"context"
	"errors"
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

func (Groq) SupportsResponses() bool        { return true }
func (Groq) SupportsMCP() bool              { return true }
func (Groq) SupportsAudioSpeech() bool      { return true }
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
		parameterCheck{"prediction", request.Prediction != nil}, parameterCheck{"verbosity", request.Verbosity != ""},
		parameterCheck{"web_search_options", request.WebSearchOptions != nil}, parameterCheck{"web_fetch_options", request.WebFetchOptions != nil},
		parameterCheck{"logprobs", request.Logprobs != nil}, parameterCheck{"top_logprobs", request.TopLogprobs != nil},
		parameterCheck{"frequency_penalty", request.FrequencyPenalty != nil}, parameterCheck{"presence_penalty", request.PresencePenalty != nil},
		parameterCheck{"min_p", request.MinP != nil}, parameterCheck{"top_k", request.TopK != nil}, parameterCheck{"top_a", request.TopA != nil},
		parameterCheck{"repetition_penalty", request.RepetitionPenalty != nil},
		parameterCheck{"logit_bias", request.LogitBias != nil},
	); err != nil {
		return err
	}
	switch request.ServiceTier {
	case "", "auto", "default", "on_demand", "flex", "performance":
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

func (g Groq) ValidateResponseParameters(request openai.ResponseRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "groq", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	if request.ServiceTier != "" && request.ServiceTier != "auto" && request.ServiceTier != "default" && request.ServiceTier != "flex" {
		return &Error{Class: FailureClientRequest, Provider: "groq", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: "service_tier", Err: errUnsupportedServiceTier}
	}
	if reasoning := request.Reasoning; reasoning != nil {
		if reasoning.Summary != nil || reasoning.GenerateSummary != nil || reasoning.Context != nil || reasoning.Mode != nil {
			return rejectParameters("groq", parameterCheck{"reasoning", true})
		}
		if reasoning.Effort != nil {
			switch *reasoning.Effort {
			case "low", "medium", "high":
			default:
				return &Error{Class: FailureClientRequest, Provider: "groq", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "reasoning.effort", Err: errors.New("reasoning effort must be low, medium, or high")}
			}
		}
	}
	return rejectParameters("groq",
		parameterCheck{"include", len(request.Include) > 0},
		parameterCheck{"store", request.Store != nil && *request.Store},
		parameterCheck{"truncation", request.Truncation != nil},
		parameterCheck{"previous_response_id", request.PreviousResponse != ""},
		parameterCheck{"safety_identifier", request.SafetyIdentifier != ""},
		parameterCheck{"prompt_cache_key", request.PromptCacheKey != ""},
		parameterCheck{"top_logprobs", request.TopLogprobs != nil},
		parameterCheck{"frequency_penalty", request.FrequencyPenalty != nil},
		parameterCheck{"presence_penalty", request.PresencePenalty != nil},
		parameterCheck{"max_tool_calls", request.MaxToolCalls != nil},
	)
}

func (g Groq) Responses(ctx context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	if err := g.ValidateResponseParameters(request); err != nil {
		return openai.ResponseResponse{}, err
	}
	return g.compatible.Responses(ctx, request)
}

func (g Groq) StreamResponses(ctx context.Context, request openai.ResponseRequest, write ResponseStreamWriter) (openai.ResponseResponse, error) {
	if err := g.ValidateResponseParameters(request); err != nil {
		return openai.ResponseResponse{}, err
	}
	return g.compatible.StreamResponses(ctx, request, write)
}

func (g Groq) GenerateSpeech(ctx context.Context, request openai.AudioSpeechRequest) (openai.AudioSpeechResponse, error) {
	if message := request.Validate(); message != "" {
		return openai.AudioSpeechResponse{}, &Error{Class: FailureClientRequest, Provider: "groq", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	if err := rejectParameters("groq",
		parameterCheck{"instructions", request.Instructions != ""},
		parameterCheck{"stream_format", request.StreamFormat != ""},
		parameterCheck{"response_format", request.ResponseFormat != "" && request.ResponseFormat != "mp3" && request.ResponseFormat != "flac" && request.ResponseFormat != "wav"},
	); err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	if request.Speed != nil && *request.Speed < 0.5 {
		return openai.AudioSpeechResponse{}, &Error{Class: FailureClientRequest, Provider: "groq", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "speed", Err: errors.New("speed must be between 0.5 and 4")}
	}
	return g.compatible.GenerateSpeech(ctx, request)
}
