package provider

import (
	"context"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

// Together exposes only the verified compatible inference operations. Keeping
// the transport private prevents unsupported API families from being inferred.
type Together struct {
	compatible OpenAICompatible
}

func NewTogether(baseURL, apiKey string, stream bool) Together {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.together.ai/v1"
	}
	compatible := NewOpenAICompatible(baseURL, apiKey, stream)
	compatible.errorProvider = "together"
	return Together{compatible: compatible}
}

func (Together) SupportsResponses() bool        { return false }
func (Together) SupportsTools() bool            { return true }
func (Together) SupportsStructuredOutput() bool { return true }
func (Together) SupportsVision() bool           { return true }

func (t Together) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if err := rejectParameters("together",
		parameterCheck{"store", request.Store != nil}, parameterCheck{"metadata", request.Metadata != nil},
		parameterCheck{"prediction", request.Prediction != nil}, parameterCheck{"service_tier", request.ServiceTier != ""},
		parameterCheck{"reasoning_effort", request.ReasoningEffort != ""}, parameterCheck{"modalities", request.Modalities != nil},
		parameterCheck{"audio", request.Audio != nil}, parameterCheck{"verbosity", request.Verbosity != ""},
		parameterCheck{"prompt_cache_key", request.PromptCacheKey != ""}, parameterCheck{"prompt_cache_options", request.PromptCacheOptions != nil},
		parameterCheck{"prompt_cache_retention", request.PromptCacheRetention != ""}, parameterCheck{"web_search_options", request.WebSearchOptions != nil},
		parameterCheck{"web_fetch_options", request.WebFetchOptions != nil}, parameterCheck{"safe_prompt", request.SafePrompt != nil},
		parameterCheck{"safety_identifier", request.SafetyIdentifier != ""}, parameterCheck{"top_a", request.TopA != nil},
		parameterCheck{"repetition_penalty", request.RepetitionPenalty != nil},
	); err != nil {
		return err
	}
	return t.compatible.ValidateChatParameters(request)
}

func (t Together) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if err := t.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return t.compatible.ChatCompletions(ctx, request)
}

func (t Together) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	if err := t.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return t.compatible.StreamChatCompletions(ctx, request, write)
}

func (Together) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, rejectParameters("together", parameterCheck{"responses", true})
}

func (t Together) ValidateCompletionParameters(request openai.CompletionRequest) error {
	return t.compatible.ValidateCompletionParameters(request)
}

func (t Together) Completions(ctx context.Context, request openai.CompletionRequest) (openai.CompletionResponse, error) {
	return t.compatible.Completions(ctx, request)
}

func (t Together) StreamCompletions(ctx context.Context, request openai.CompletionRequest, write CompletionStreamWriter) (openai.CompletionResponse, error) {
	return t.compatible.StreamCompletions(ctx, request, write)
}

func (t Together) ValidateEmbeddingParameters(request openai.EmbeddingRequest) error {
	if err := rejectParameters("together",
		parameterCheck{"dimensions", request.Dimensions != nil}, parameterCheck{"user", request.User != ""},
		parameterCheck{"encoding_format", request.EncodingFormat != "" && request.EncodingFormat != "float"},
		parameterCheck{"input_type", request.InputType != ""}, parameterCheck{"output_dtype", request.OutputDType != ""},
	); err != nil {
		return err
	}
	return t.compatible.ValidateEmbeddingParameters(request)
}

func (t Together) Embeddings(ctx context.Context, request openai.EmbeddingRequest) (openai.EmbeddingResponse, error) {
	if err := t.ValidateEmbeddingParameters(request); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	return t.compatible.Embeddings(ctx, request)
}
