package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

func (Groq) SupportsResponses() bool          { return true }
func (Groq) SupportsMCP() bool                { return true }
func (Groq) SupportsAudioTranscription() bool { return true }
func (Groq) SupportsAudioTranslation() bool   { return true }
func (Groq) SupportsAudioSpeech() bool        { return true }
func (Groq) SupportsTools() bool              { return true }
func (Groq) SupportsStructuredOutput() bool   { return true }
func (Groq) SupportsVision() bool             { return true }

func (Groq) ManagedChatModelProbes() []string {
	return []string{"openai/gpt-oss-20b", "openai/gpt-oss-120b", "qwen/qwen3.8-27b"}
}

func (g Groq) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if err := rejectChatModeration("groq", request); err != nil {
		return err
	}
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
	if err := validateGroqReasoningControls(request); err != nil {
		return err
	}
	switch request.ServiceTier {
	case "", "auto", "on_demand", "flex", "performance":
	default:
		return &Error{Class: FailureClientRequest, Provider: "groq", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: "service_tier", Err: errUnsupportedServiceTier}
	}
	return g.compatible.ValidateChatParameters(request)
}

func validateGroqReasoningControls(request openai.ChatCompletionRequest) error {
	invalid := func(parameter, message string) error {
		return &Error{Class: FailureClientRequest, Provider: "groq", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: parameter, Err: errors.New(message)}
	}
	if request.IncludeReasoning != nil {
		switch request.Model {
		case "openai/gpt-oss-20b", "openai/gpt-oss-120b":
		default:
			return invalid("include_reasoning", "include_reasoning is not supported by this Groq model")
		}
	}
	if request.ReasoningFormat != "" {
		if request.Model != "qwen/qwen3.8-27b" {
			return invalid("reasoning_format", "reasoning_format is not supported by this Groq model")
		}
		if request.ReasoningFormat == "raw" && (len(request.Tools) > 0 || request.ResponseFormat != nil && request.ResponseFormat.Type != "text") {
			return invalid("reasoning_format", "reasoning_format=raw cannot be combined with tools or JSON response formats")
		}
	}
	if request.ReasoningEffort != "" {
		valid := false
		switch request.Model {
		case "openai/gpt-oss-20b", "openai/gpt-oss-120b":
			valid = request.ReasoningEffort == "low" || request.ReasoningEffort == "medium" || request.ReasoningEffort == "high"
		case "qwen/qwen3.8-27b":
			valid = request.ReasoningEffort == "none" || request.ReasoningEffort == "default" || request.ReasoningEffort == "low" || request.ReasoningEffort == "medium" || request.ReasoningEffort == "high"
		}
		if !valid {
			return invalid("reasoning_effort", "reasoning_effort is not supported by this Groq model")
		}
	}
	return nil
}

func (g Groq) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if err := g.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return g.compatible.chatCompletions(ctx, request, decodeGroqChatCompletionResponse, normalizeGroqChatStreamPayload)
}

func (g Groq) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	if err := g.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return g.compatible.streamChatCompletions(ctx, request, write, normalizeGroqChatStreamPayload)
}

func decodeGroqChatCompletionResponse(reader io.Reader, target *openai.ChatCompletionResponse) error {
	payload, err := io.ReadAll(io.LimitReader(reader, maxChatCompletionResponseBytes+1))
	if err != nil {
		return err
	}
	if len(payload) > maxChatCompletionResponseBytes {
		return errors.New("chat completion response exceeds limit")
	}
	normalized, err := normalizeChatReasoningAliasPayload("Groq", payload, "message")
	if err != nil {
		return err
	}
	return json.Unmarshal(normalized, target)
}

func normalizeGroqChatStreamPayload(payload string) (string, error) {
	normalized, err := normalizeChatReasoningAliasPayload("Groq", []byte(payload), "delta")
	return string(normalized), err
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
	_, verbositySupplied := openai.ResponseTextVerbosity(request.Text)
	return rejectParameters("groq",
		parameterCheck{"context_management", len(request.ContextManagement) > 0},
		parameterCheck{"moderation", request.Moderation != nil},
		parameterCheck{"include", len(request.Include) > 0},
		parameterCheck{"store", request.Store != nil && *request.Store},
		parameterCheck{"truncation", request.Truncation != nil},
		parameterCheck{"previous_response_id", request.PreviousResponse != ""},
		parameterCheck{"safety_identifier", request.SafetyIdentifier != ""},
		parameterCheck{"prompt_cache_key", request.PromptCacheKey != ""},
		parameterCheck{"prompt_cache_options", request.PromptCacheOptions != nil},
		parameterCheck{"prompt_cache_retention", request.PromptCacheRetention != ""},
		parameterCheck{"stream_options", request.StreamOptions != nil},
		parameterCheck{"text.verbosity", verbositySupplied},
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

func (g Groq) ValidateAudioSpeechParameters(request openai.AudioSpeechRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "groq", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	if err := rejectParameters("groq",
		parameterCheck{"language", request.Language != ""},
		parameterCheck{"instructions", request.Instructions != ""},
		parameterCheck{"stream_format", request.StreamFormat != ""},
		parameterCheck{"response_format", request.ResponseFormat != "" && request.ResponseFormat != "mp3" && request.ResponseFormat != "flac" && request.ResponseFormat != "wav"},
	); err != nil {
		return err
	}
	if request.Speed != nil && *request.Speed < 0.5 {
		return &Error{Class: FailureClientRequest, Provider: "groq", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "speed", Err: errors.New("speed must be between 0.5 and 4")}
	}
	return nil
}

func (g Groq) GenerateSpeech(ctx context.Context, request openai.AudioSpeechRequest) (openai.AudioSpeechResponse, error) {
	if err := g.ValidateAudioSpeechParameters(request); err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	return g.compatible.GenerateSpeech(ctx, request)
}
