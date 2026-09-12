package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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
func (Together) SupportsAudioSpeech() bool      { return true }
func (Together) SupportsAudioSpeechStreaming() bool {
	return false
}

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
		parameterCheck{"repetition_penalty", request.RepetitionPenalty != nil}, parameterCheck{"logprobs", request.Logprobs != nil},
		parameterCheck{"top_logprobs", request.TopLogprobs != nil}, parameterCheck{"logit_bias", request.LogitBias != nil},
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

func (Together) ValidateAudioSpeechParameters(request openai.AudioSpeechRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "together", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	return rejectParameters("together",
		parameterCheck{"language", request.Language != "" && (strings.EqualFold(request.Language, "auto") || request.Language != strings.ToLower(request.Language))},
		parameterCheck{"instructions", request.Instructions != ""},
		parameterCheck{"response_format", request.ResponseFormat != "" && request.ResponseFormat != "mp3" && request.ResponseFormat != "wav" && request.ResponseFormat != "pcm"},
		parameterCheck{"speed", request.Speed != nil},
		parameterCheck{"stream_format", request.StreamFormat == "sse"},
	)
}

func (t Together) GenerateSpeech(ctx context.Context, request openai.AudioSpeechRequest) (openai.AudioSpeechResponse, error) {
	if err := t.ValidateAudioSpeechParameters(request); err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	responseFormat := request.ResponseFormat
	if responseFormat == "" {
		responseFormat = "mp3"
	} else if responseFormat == "pcm" {
		responseFormat = "raw"
	}
	payload, err := json.Marshal(struct {
		Model          string `json:"model"`
		Input          string `json:"input"`
		Voice          string `json:"voice"`
		Language       string `json:"language,omitempty"`
		ResponseFormat string `json:"response_format"`
		Stream         bool   `json:"stream"`
	}{request.Model, request.Input, request.Voice, request.Language, responseFormat, false})
	if err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	response, err := t.compatible.sendBufferedSpeech(ctx, request, payload)
	if err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	response.ContentType = request.ExpectedContentType()
	return response, nil
}

func (Together) ValidateRerankParameters(request openai.RerankRequest) error {
	return rejectParameters("together",
		parameterCheck{"rank_fields", len(request.RankFields) > 0},
		parameterCheck{"max_chunks_per_doc", request.MaxChunksPerDoc != nil},
		parameterCheck{"max_tokens_per_doc", request.MaxTokensPerDoc != nil},
	)
}

func (t Together) Rerank(ctx context.Context, request openai.RerankRequest) (openai.RerankResponse, error) {
	if err := t.ValidateRerankParameters(request); err != nil {
		return openai.RerankResponse{}, err
	}
	body, err := json.Marshal(openAICompatibleRerankRequest{Model: request.Model, Query: request.Query, Documents: request.Documents, TopN: request.TopN, ReturnDocuments: request.ReturnDocuments})
	if err != nil {
		return openai.RerankResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(t.compatible.baseURL, "rerank"), bytes.NewReader(body))
	if err != nil {
		return openai.RerankResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if t.compatible.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+t.compatible.apiKey)
	}
	response, err := t.compatible.client.Do(httpRequest)
	if err != nil {
		return openai.RerankResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.RerankResponse{}, responseStatusError("together", response)
	}
	var wire struct {
		ID      string                `json:"id"`
		Results []openai.RerankResult `json:"results"`
		Usage   *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil {
		return openai.RerankResponse{}, err
	}
	if len(payload) > 8<<20 {
		return openai.RerankResponse{}, errors.New("together rerank response exceeds 8 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&wire); err != nil {
		return openai.RerankResponse{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return openai.RerankResponse{}, errors.New("invalid trailing rerank response data")
	}
	if wire.Usage == nil || wire.Usage.PromptTokens < 0 || wire.Usage.CompletionTokens < 0 || wire.Usage.TotalTokens != wire.Usage.PromptTokens+wire.Usage.CompletionTokens {
		return openai.RerankResponse{}, errors.New("invalid together rerank usage")
	}
	return openai.RerankResponse{
		ID:      wire.ID,
		Results: wire.Results,
		Meta: &openai.RerankResponseMeta{Tokens: &openai.RerankTokens{
			InputTokens:  wire.Usage.PromptTokens,
			OutputTokens: wire.Usage.CompletionTokens,
		}},
	}, nil
}
