package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

type Mistral struct {
	OpenAICompatible
}

func (Mistral) SupportsAudioSpeechStreaming() bool { return false }

func (Mistral) StreamGenerateSpeech(context.Context, openai.AudioSpeechRequest, AudioSpeechStreamWriter) (openai.AudioSpeechResponse, error) {
	return openai.AudioSpeechResponse{}, ErrStreamingUnsupported
}

type mistralFIMRequest struct {
	Model          string            `json:"model"`
	Prompt         string            `json:"prompt"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Suffix         string            `json:"suffix,omitempty"`
	MaxTokens      *int              `json:"max_tokens,omitempty"`
	MinTokens      *int              `json:"min_tokens,omitempty"`
	PromptCacheKey string            `json:"prompt_cache_key,omitempty"`
	RandomSeed     *int64            `json:"random_seed,omitempty"`
	Stop           any               `json:"stop,omitempty"`
	Stream         bool              `json:"stream"`
	Temperature    *float64          `json:"temperature,omitempty"`
	TopP           *float64          `json:"top_p,omitempty"`
}

type mistralFIMResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int            `json:"index"`
		Message      openai.Message `json:"message"`
		Delta        openai.Message `json:"delta"`
		FinishReason *string        `json:"finish_reason"`
	} `json:"choices"`
	Usage *openai.Usage `json:"usage,omitempty"`
}

func NewMistral(baseURL, apiKey string, upstreamStream bool) Mistral {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.mistral.ai"
	}
	compatible := NewOpenAICompatible(baseURL, apiKey, upstreamStream)
	compatible.errorProvider = "mistral"
	compatible.supportsSafePrompt = true
	compatible.supportsPromptMode = true
	compatible.supportsMessagePrefix = true
	return Mistral{OpenAICompatible: compatible}
}

func (Mistral) SupportsResponses() bool       { return false }
func (Mistral) SupportsRerank() bool          { return false }
func (Mistral) SupportsImageGeneration() bool { return false }

func (Mistral) SupportsImageEdit() bool { return false }

func (Mistral) SupportsImageVariation() bool { return false }

func (Mistral) SupportsAudioTranscription() bool { return true }

func (Mistral) SupportsAudioSpeech() bool { return true }

func (Mistral) SupportsSearch() bool { return false }

func (Mistral) GenerateImage(context.Context, openai.ImageGenerationRequest) (openai.ImageGenerationResponse, error) {
	return openai.ImageGenerationResponse{}, &Error{Class: FailureClientRequest, Provider: "mistral", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_operation", Err: errors.New("image generation is not supported by this adapter")}
}

func (Mistral) EditImage(context.Context, openai.ImageEditRequest) (openai.ImageGenerationResponse, error) {
	return openai.ImageGenerationResponse{}, &Error{Class: FailureClientRequest, Provider: "mistral", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_operation", Err: errors.New("image edits are not supported by this adapter")}
}

func (Mistral) CreateImageVariation(context.Context, openai.ImageVariationRequest) (openai.ImageGenerationResponse, error) {
	return openai.ImageGenerationResponse{}, &Error{Class: FailureClientRequest, Provider: "mistral", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_operation", Err: errors.New("image variations are not supported by this adapter")}
}

func (Mistral) ValidateAudioSpeechParameters(request openai.AudioSpeechRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "mistral", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	return rejectParameters("mistral",
		parameterCheck{"language", request.Language != ""},
		parameterCheck{"instructions", request.Instructions != ""},
		parameterCheck{"speed", request.Speed != nil},
		parameterCheck{"stream_format", request.StreamFormat != ""},
		parameterCheck{"response_format", request.ResponseFormat == "aac"},
	)
}

func (p Mistral) GenerateSpeech(ctx context.Context, request openai.AudioSpeechRequest) (openai.AudioSpeechResponse, error) {
	if err := p.ValidateAudioSpeechParameters(request); err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	responseFormat := request.ResponseFormat
	if responseFormat == "" {
		responseFormat = "mp3"
	}
	payload, err := json.Marshal(struct {
		Input          string `json:"input"`
		Model          string `json:"model"`
		VoiceID        string `json:"voice_id"`
		ResponseFormat string `json:"response_format,omitempty"`
		Stream         bool   `json:"stream"`
	}{Input: request.Input, Model: request.Model, VoiceID: request.Voice, ResponseFormat: responseFormat})
	if err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "audio/speech"), bytes.NewReader(payload))
	if err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.AudioSpeechResponse{}, responseStatusError("mistral", response)
	}
	data, err := decodeMistralAudioSpeech(response.Body)
	if err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	return openai.AudioSpeechResponse{Data: data, ContentType: request.ExpectedContentType(), Model: request.Model}, nil
}

const maxMistralAudioSpeechResponseBytes = 44 << 20

func decodeMistralAudioSpeech(reader io.Reader) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxMistralAudioSpeechResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > maxMistralAudioSpeechResponseBytes {
		return nil, errors.New("Mistral audio speech response exceeds limit")
	}
	var response *struct {
		AudioData string `json:"audio_data"`
	}
	if err := json.Unmarshal(payload, &response); err != nil || response == nil || response.AudioData == "" {
		return nil, errors.New("invalid Mistral audio speech response")
	}
	if base64.StdEncoding.DecodedLen(len(response.AudioData)) > maxAudioSpeechResponseBytes {
		return nil, errors.New("Mistral audio speech response exceeds decoded limit")
	}
	data, err := base64.StdEncoding.DecodeString(response.AudioData)
	if err != nil || len(data) == 0 || len(data) > maxAudioSpeechResponseBytes {
		return nil, errors.New("invalid Mistral audio speech data")
	}
	return data, nil
}

func (p Mistral) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if err := rejectLegacyFunctionCalling("mistral", request); err != nil {
		return err
	}
	if err := validateChatPromptCacheBreakpoints("mistral", request, false); err != nil {
		return err
	}
	if err := rejectToolCallMetadata("mistral", request.Messages); err != nil {
		return err
	}
	if err := rejectChatMessageRefusals("mistral", request.Messages); err != nil {
		return err
	}
	if err := rejectChatMessageAudio("mistral", request.Messages); err != nil {
		return err
	}
	options := request.ChatGenerationOptions
	if err := rejectParameters("mistral",
		parameterCheck{"store", options.Store != nil},
		parameterCheck{"modalities", options.Modalities != nil},
		parameterCheck{"audio", options.Audio != nil},
		parameterCheck{"safety_identifier", options.SafetyIdentifier != ""},
		parameterCheck{"prompt_cache_options", options.PromptCacheOptions != nil},
		parameterCheck{"prompt_cache_retention", options.PromptCacheRetention != ""},
		parameterCheck{"user", options.User != ""},
		parameterCheck{"verbosity", options.Verbosity != ""},
		parameterCheck{"top_logprobs", options.TopLogprobs != nil},
		parameterCheck{"logprobs", options.Logprobs != nil},
		parameterCheck{"min_p", options.MinP != nil},
		parameterCheck{"top_k", options.TopK != nil},
		parameterCheck{"top_a", options.TopA != nil},
		parameterCheck{"repetition_penalty", options.RepetitionPenalty != nil},
		parameterCheck{"logit_bias", options.LogitBias != nil},
		parameterCheck{"stream_options.include_obfuscation", request.StreamOptions != nil && request.StreamOptions.IncludeObfuscation != nil},
	); err != nil {
		return err
	}
	if err := p.OpenAICompatible.ValidateChatParameters(request); err != nil {
		return err
	}
	if request.ReasoningEffort == "max" || request.ReasoningEffort == "default" {
		return &Error{Class: FailureClientRequest, Provider: "mistral", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "reasoning_effort", Err: errors.New("reasoning_effort must be none, minimal, low, medium, high, or xhigh")}
	}
	return rejectParameters("mistral", parameterCheck{"web_search_options", request.WebSearchOptions != nil}, parameterCheck{"web_fetch_options", request.WebFetchOptions != nil})
}

func (p Mistral) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if err := p.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return p.OpenAICompatible.ChatCompletions(ctx, request)
}

func (p Mistral) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	if err := p.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return p.OpenAICompatible.StreamChatCompletions(ctx, request, write)
}

func (Mistral) ValidateEmbeddingParameters(request openai.EmbeddingRequest) error {
	if message := openai.ValidateMetadata(request.Metadata); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "mistral", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "metadata", Err: errors.New(message)}
	}
	switch request.OutputDType {
	case "", "float", "int8", "uint8", "binary", "ubinary":
	default:
		return &Error{Class: FailureClientRequest, Provider: "mistral", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "output_dtype", Err: errors.New("output_dtype must be float, int8, uint8, binary, or ubinary")}
	}
	if request.EncodingFormat != "" && request.EncodingFormat != "float" && request.EncodingFormat != "base64" {
		return &Error{Class: FailureClientRequest, Provider: "mistral", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "encoding_format", Err: errors.New("encoding_format must be float or base64")}
	}
	if request.Dimensions != nil && (*request.Dimensions <= 0 || *request.Dimensions > 3072 || ((request.OutputDType == "binary" || request.OutputDType == "ubinary") && *request.Dimensions%8 != 0)) {
		return &Error{Class: FailureClientRequest, Provider: "mistral", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "dimensions", Err: errors.New("Mistral dimensions must not exceed 3072 and must be divisible by 8 for binary output")}
	}
	input, err := openai.InspectEmbeddingInput(request.Input)
	return rejectParameters("mistral",
		parameterCheck{"input", err != nil || input.Tokenized()},
		parameterCheck{"input_type", request.InputType != ""},
		parameterCheck{"user", request.User != ""},
	)
}

func (p Mistral) Embeddings(ctx context.Context, request openai.EmbeddingRequest) (openai.EmbeddingResponse, error) {
	if err := p.ValidateEmbeddingParameters(request); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	return p.OpenAICompatible.Embeddings(ctx, request)
}

func (Mistral) ValidateModerationParameters(request openai.ModerationRequest) error {
	if message := openai.ValidateMetadata(request.Metadata); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "mistral", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "metadata", Err: errors.New(message)}
	}
	if !mistralModerationTextInput(request.Input) {
		return &Error{Class: FailureClientRequest, Provider: "mistral", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: "input", Err: errors.New("Mistral moderation requires text input")}
	}
	return nil
}

func (p Mistral) Moderations(ctx context.Context, request openai.ModerationRequest) (openai.ModerationResponse, error) {
	if err := p.ValidateModerationParameters(request); err != nil {
		return openai.ModerationResponse{}, err
	}
	return p.OpenAICompatible.Moderations(ctx, request)
}

func mistralModerationTextInput(input any) bool {
	switch value := input.(type) {
	case string:
		return strings.TrimSpace(value) != ""
	case []string:
		if len(value) == 0 || len(value) > openai.MaxModerationInputs {
			return false
		}
		for _, item := range value {
			if strings.TrimSpace(item) == "" {
				return false
			}
		}
		return true
	case []any:
		if len(value) == 0 || len(value) > openai.MaxModerationInputs {
			return false
		}
		for _, item := range value {
			text, ok := item.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func (Mistral) ValidateCompletionParameters(request openai.CompletionRequest) error {
	if message := openai.ValidateMetadata(request.Metadata); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "mistral", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "metadata", Err: errors.New(message)}
	}
	if request.MinTokens != nil && (*request.MinTokens < 0 || request.MaxTokens != nil && *request.MinTokens > *request.MaxTokens) {
		return &Error{Class: FailureClientRequest, Provider: "mistral", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "min_tokens", Err: errors.New("min_tokens must be nonnegative and not exceed max_tokens")}
	}
	prompt, err := openai.InspectCompletionPrompt(request.Prompt)
	if err != nil || prompt.Kind != openai.CompletionPromptText {
		return &Error{Class: FailureClientRequest, Provider: "mistral", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: "prompt", Err: errors.New("Mistral FIM requires one string prompt")}
	}
	return rejectParameters("mistral",
		parameterCheck{"best_of", request.BestOf != nil},
		parameterCheck{"echo", request.Echo != nil},
		parameterCheck{"frequency_penalty", request.FrequencyPenalty != nil},
		parameterCheck{"logit_bias", request.LogitBias != nil},
		parameterCheck{"logprobs", request.Logprobs != nil},
		parameterCheck{"n", request.N != nil},
		parameterCheck{"presence_penalty", request.PresencePenalty != nil},
		parameterCheck{"user", request.User != ""},
	)
}

func (p Mistral) Completions(ctx context.Context, request openai.CompletionRequest) (openai.CompletionResponse, error) {
	return p.fimCompletion(ctx, request, false, nil)
}

func (p Mistral) StreamCompletions(ctx context.Context, request openai.CompletionRequest, write CompletionStreamWriter) (openai.CompletionResponse, error) {
	if !p.upstreamStream {
		return openai.CompletionResponse{}, ErrStreamingUnsupported
	}
	return p.fimCompletion(ctx, request, true, write)
}

func (p Mistral) fimCompletion(ctx context.Context, request openai.CompletionRequest, stream bool, write CompletionStreamWriter) (openai.CompletionResponse, error) {
	if err := p.ValidateCompletionParameters(request); err != nil {
		return openai.CompletionResponse{}, err
	}
	prompt, _ := request.Prompt.(string)
	body, err := json.Marshal(mistralFIMRequest{
		Model: request.Model, Prompt: prompt, Metadata: request.Metadata, Suffix: request.Suffix,
		MaxTokens: request.MaxTokens, MinTokens: request.MinTokens, PromptCacheKey: request.PromptCacheKey,
		RandomSeed: request.Seed, Stop: request.Stop, Stream: stream, Temperature: request.Temperature, TopP: request.TopP,
	})
	if err != nil {
		return openai.CompletionResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "fim/completions"), bytes.NewReader(body))
	if err != nil {
		return openai.CompletionResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	if !stream {
		httpRequest.Header.Set("Accept", "application/json")
	}
	if p.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return openai.CompletionResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return openai.CompletionResponse{}, responseStatusError("mistral", response)
	}
	if stream {
		return streamMistralFIM(response.Body, request, write)
	}
	return decodeMistralFIM(response.Body, request)
}

func decodeMistralFIM(reader io.Reader, request openai.CompletionRequest) (openai.CompletionResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxResponseJSONBytes+1))
	if err != nil {
		return openai.CompletionResponse{}, err
	}
	if len(payload) > maxResponseJSONBytes {
		return openai.CompletionResponse{}, errors.New("Mistral FIM response exceeds 32 MiB")
	}
	var upstream mistralFIMResponse
	if err := json.Unmarshal(payload, &upstream); err != nil {
		return openai.CompletionResponse{}, err
	}
	return mistralFIMCompletion(upstream, request)
}

func mistralFIMCompletion(upstream mistralFIMResponse, request openai.CompletionRequest) (openai.CompletionResponse, error) {
	response := openai.CompletionResponse{ID: upstream.ID, Object: "text_completion", Created: upstream.Created, Model: upstream.Model}
	if upstream.Object != "chat.completion" || upstream.Usage == nil {
		return openai.CompletionResponse{}, errors.New("invalid Mistral FIM response envelope")
	}
	response.Usage = *upstream.Usage
	for _, choice := range upstream.Choices {
		text, ok := choice.Message.Content.(string)
		if !ok || choice.FinishReason == nil || choice.Message.Role != "assistant" || choice.Message.Name != "" || choice.Message.ToolCallID != "" || len(choice.Message.ToolCalls) > 0 || choice.Message.FunctionCall != nil || choice.Message.Audio != nil || choice.Message.Refusal != nil || len(choice.Message.Annotations) > 0 {
			return openai.CompletionResponse{}, errors.New("invalid Mistral FIM completion choice")
		}
		response.Choices = append(response.Choices, openai.CompletionChoice{Index: choice.Index, Text: text, FinishReason: *choice.FinishReason})
	}
	if err := validateCompletionResult(response, request); err != nil {
		return openai.CompletionResponse{}, err
	}
	return response, nil
}

func streamMistralFIM(reader io.Reader, request openai.CompletionRequest, write CompletionStreamWriter) (openai.CompletionResponse, error) {
	response := openai.CompletionResponse{Object: "text_completion"}
	seenDone := false
	err := scanSSEData(&responseStreamReader{source: reader, remaining: maxResponseStreamBytes}, func(payload string) error {
		if payload == "[DONE]" {
			seenDone = true
			return io.EOF
		}
		var upstream mistralFIMResponse
		if err := json.Unmarshal([]byte(payload), &upstream); err != nil {
			return err
		}
		if upstream.Object != "chat.completion.chunk" {
			return errors.New("invalid Mistral FIM stream object")
		}
		if err := mergeMistralFIMIdentity(&response, upstream); err != nil {
			return err
		}
		if upstream.Usage != nil {
			if err := validateCompletionUsage(*upstream.Usage); err != nil {
				return err
			}
			response.Usage = *upstream.Usage
		}
		for _, choice := range upstream.Choices {
			text := ""
			if choice.Delta.Content != nil {
				var ok bool
				text, ok = choice.Delta.Content.(string)
				if !ok {
					return errors.New("invalid Mistral FIM stream content")
				}
			}
			if choice.Index != 0 || choice.Delta.Role != "" && choice.Delta.Role != "assistant" || choice.Delta.Name != "" || choice.Delta.ToolCallID != "" || len(choice.Delta.ToolCalls) > 0 || choice.Delta.FunctionCall != nil || choice.Delta.Audio != nil || choice.Delta.Refusal != nil || len(choice.Delta.Annotations) > 0 {
				return errors.New("invalid Mistral FIM stream choice")
			}
			if response.ID == "" || response.Model == "" || response.Created <= 0 {
				return errors.New("incomplete Mistral FIM stream identity")
			}
			if len(response.Choices) == 0 {
				response.Choices = append(response.Choices, openai.CompletionChoice{Index: 0})
			}
			response.Choices[0].Text += text
			finish := any(nil)
			if choice.FinishReason != nil {
				response.Choices[0].FinishReason = *choice.FinishReason
				finish = *choice.FinishReason
			}
			if write != nil {
				chunk, err := json.Marshal(map[string]any{
					"id": response.ID, "object": "text_completion", "created": response.Created, "model": response.Model,
					"choices": []map[string]any{{"index": 0, "text": text, "logprobs": nil, "finish_reason": finish}},
				})
				if err != nil {
					return err
				}
				if err := write(string(chunk)); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return openai.CompletionResponse{}, err
	}
	if !seenDone {
		return openai.CompletionResponse{}, errors.New("Mistral FIM stream ended before [DONE]")
	}
	if err := validateCompletionResult(response, request); err != nil {
		return openai.CompletionResponse{}, err
	}
	return response, nil
}

func mergeMistralFIMIdentity(response *openai.CompletionResponse, upstream mistralFIMResponse) error {
	if upstream.ID != "" {
		if response.ID != "" && response.ID != upstream.ID {
			return errors.New("Mistral FIM changed stream ID")
		}
		response.ID = upstream.ID
	}
	if upstream.Model != "" {
		if response.Model != "" && response.Model != upstream.Model {
			return errors.New("Mistral FIM changed stream model")
		}
		response.Model = upstream.Model
	}
	if upstream.Created != 0 {
		if upstream.Created < 0 || response.Created != 0 && response.Created != upstream.Created {
			return errors.New("Mistral FIM changed stream timestamp")
		}
		response.Created = upstream.Created
	}
	return nil
}
