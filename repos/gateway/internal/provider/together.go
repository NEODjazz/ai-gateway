package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"strconv"
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
	compatible.chatMessages = togetherChatMessages
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
func (Together) SupportsAudioTranscription() bool { return true }
func (Together) SupportsAudioTranscriptionStreaming() bool {
	return false
}
func (Together) SupportsAudioTranslation() bool { return true }
func (Together) SupportsImageGeneration() bool  { return true }
func (Together) UsesImageUnitUsage() bool       { return true }

func (Together) ValidateImageGenerationParameters(request openai.ImageGenerationRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "together", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	if request.N != nil && *request.N > 4 {
		return &Error{Class: FailureClientRequest, Provider: "together", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New("n must be between 1 and 4 for together")}
	}
	return rejectParameters("together",
		parameterCheck{"quality", request.Quality != ""},
		parameterCheck{"style", request.Style != ""},
		parameterCheck{"user", request.User != ""},
		parameterCheck{"background", request.Background != ""},
		parameterCheck{"output_format", request.OutputFormat != "" && request.OutputFormat != "jpeg" && request.OutputFormat != "png"},
		parameterCheck{"output_compression", request.OutputCompression != nil},
		parameterCheck{"resolution", request.Resolution != ""},
		parameterCheck{"aspect_ratio", request.AspectRatio != ""},
		parameterCheck{"stream", request.Stream},
		parameterCheck{"partial_images", request.PartialImages != nil},
	)
}

func (t Together) GenerateImage(ctx context.Context, request openai.ImageGenerationRequest) (openai.ImageGenerationResponse, error) {
	if err := t.ValidateImageGenerationParameters(request); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	responseFormat := request.ResponseFormat
	if responseFormat == "b64_json" {
		responseFormat = "base64"
	}
	var width, height *int
	if request.Size != "" && request.Size != "auto" {
		widthText, heightText, _ := strings.Cut(request.Size, "x")
		widthValue, widthErr := strconv.Atoi(widthText)
		heightValue, heightErr := strconv.Atoi(heightText)
		if widthErr != nil || heightErr != nil {
			return openai.ImageGenerationResponse{}, rejectParameters("together", parameterCheck{"size", true})
		}
		width, height = &widthValue, &heightValue
	}
	body, err := json.Marshal(struct {
		Model          string `json:"model"`
		Prompt         string `json:"prompt"`
		N              *int   `json:"n,omitempty"`
		Width          *int   `json:"width,omitempty"`
		Height         *int   `json:"height,omitempty"`
		ResponseFormat string `json:"response_format,omitempty"`
		OutputFormat   string `json:"output_format,omitempty"`
		Seed           *int64 `json:"seed,omitempty"`
	}{request.Model, request.Prompt, request.N, width, height, responseFormat, request.OutputFormat, request.Seed})
	if err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(t.compatible.baseURL, "images/generations"), bytes.NewReader(body))
	if err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if t.compatible.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+t.compatible.apiKey)
	}
	response, err := t.compatible.client.Do(httpRequest)
	if err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.ImageGenerationResponse{}, responseStatusError("together", response)
	}
	return decodeTogetherImageResponse(response.Body, request)
}

func decodeTogetherImageResponse(reader io.Reader, request openai.ImageGenerationRequest) (openai.ImageGenerationResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxImageGenerationResponseBytes+1))
	if err != nil || len(payload) > maxImageGenerationResponseBytes {
		return openai.ImageGenerationResponse{}, errors.New("together image response exceeds limit")
	}
	var wire struct {
		ID     string `json:"id"`
		Model  string `json:"model"`
		Object string `json:"object"`
		Data   []struct {
			Index   int    `json:"index"`
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&wire); err != nil {
		return openai.ImageGenerationResponse{}, errors.New("together image response must be an object")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return openai.ImageGenerationResponse{}, errors.New("invalid trailing together image response data")
	}
	if strings.TrimSpace(wire.ID) == "" || wire.Model != request.Model || wire.Object != "list" {
		return openai.ImageGenerationResponse{}, errors.New("together returned invalid image metadata")
	}
	result := openai.ImageGenerationResponse{Data: make([]openai.ImageData, len(wire.Data))}
	for index, image := range wire.Data {
		if image.Index != index {
			return openai.ImageGenerationResponse{}, errors.New("together returned invalid image indices")
		}
		if request.ResponseFormat == "b64_json" && image.B64JSON == "" || request.ResponseFormat == "url" && image.URL == "" {
			return openai.ImageGenerationResponse{}, errors.New("together returned an unexpected image response format")
		}
		result.Data[index] = openai.ImageData{B64JSON: image.B64JSON, URL: image.URL}
	}
	if err := validateImageGenerationUnitResponse(result, request); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	return result, nil
}

func (t Together) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	if request.ReasoningEffort != "" {
		allowed := map[string]bool{}
		switch request.Model {
		case "openai/gpt-oss-20b", "openai/gpt-oss-120b":
			allowed = map[string]bool{"low": true, "medium": true, "high": true}
		case "deepseek-ai/DeepSeek-V4-Pro-0813":
			allowed = map[string]bool{"high": true, "max": true}
		}
		if !allowed[request.ReasoningEffort] {
			return &Error{Class: FailureClientRequest, Provider: "together", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: "reasoning_effort", Err: errors.New("reasoning_effort is not supported for this Together model or value")}
		}
	}
	if err := rejectParameters("together",
		parameterCheck{"store", request.Store != nil}, parameterCheck{"metadata", request.Metadata != nil},
		parameterCheck{"prediction", request.Prediction != nil}, parameterCheck{"service_tier", request.ServiceTier != ""},
		parameterCheck{"modalities", request.Modalities != nil},
		parameterCheck{"audio", request.Audio != nil}, parameterCheck{"verbosity", request.Verbosity != ""},
		parameterCheck{"prompt_cache_key", request.PromptCacheKey != ""}, parameterCheck{"prompt_cache_options", request.PromptCacheOptions != nil},
		parameterCheck{"prompt_cache_retention", request.PromptCacheRetention != ""}, parameterCheck{"web_search_options", request.WebSearchOptions != nil},
		parameterCheck{"web_fetch_options", request.WebFetchOptions != nil}, parameterCheck{"safe_prompt", request.SafePrompt != nil},
		parameterCheck{"safety_identifier", request.SafetyIdentifier != ""}, parameterCheck{"top_a", request.TopA != nil},
		parameterCheck{"repetition_penalty", request.RepetitionPenalty != nil}, parameterCheck{"top_logprobs", request.TopLogprobs != nil},
		parameterCheck{"logit_bias", request.LogitBias != nil},
	); err != nil {
		return err
	}
	return t.compatible.ValidateChatParameters(request)
}

func (Together) ManagedChatModelProbes() []string {
	return []string{"openai/gpt-oss-20b", "openai/gpt-oss-120b", "deepseek-ai/DeepSeek-V4-Pro-0813"}
}

func togetherChatMessages(request openai.ChatCompletionRequest) (any, error) {
	if request.Model != "openai/gpt-oss-20b" && request.Model != "openai/gpt-oss-120b" && request.Model != "deepseek-ai/DeepSeek-V4-Pro-0813" {
		return nil, nil
	}
	messages := make([]map[string]json.RawMessage, len(request.Messages))
	for index, message := range request.Messages {
		payload, err := json.Marshal(message)
		if err != nil || json.Unmarshal(payload, &messages[index]) != nil {
			return nil, errors.New("failed to encode Together chat message")
		}
		if reasoning, found := messages[index]["reasoning_content"]; found {
			delete(messages[index], "reasoning_content")
			messages[index]["reasoning"] = reasoning
		}
	}
	return messages, nil
}

func decodeTogetherChatCompletionResponse(reader io.Reader, target *openai.ChatCompletionResponse) error {
	payload, err := io.ReadAll(io.LimitReader(reader, maxChatCompletionResponseBytes+1))
	if err != nil {
		return err
	}
	if len(payload) > maxChatCompletionResponseBytes {
		return errors.New("chat completion response exceeds limit")
	}
	normalized, err := normalizeTogetherChatPayload(payload, "message")
	if err != nil {
		return err
	}
	return json.Unmarshal(normalized, target)
}

func normalizeTogetherChatStreamPayload(payload string) (string, error) {
	return normalizeTogetherChatStreamPayloadRequired(payload, false)
}

func normalizeTogetherChatStreamPayloadRequired(payload string, requireLogprobs bool) (string, error) {
	normalized, err := normalizeTogetherChatPayload([]byte(payload), "delta")
	if err != nil || !requireLogprobs {
		return string(normalized), err
	}
	var envelope struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
			Logprobs *openai.ChoiceLogprobs `json:"logprobs"`
		} `json:"choices"`
	}
	if json.Unmarshal(normalized, &envelope) != nil {
		return "", errors.New("provider returned invalid Together stream logprobs")
	}
	for _, choice := range envelope.Choices {
		if choice.Delta.Content != "" && (choice.Logprobs == nil || len(choice.Logprobs.Content) == 0) {
			return "", errors.New("provider omitted requested Together stream logprobs")
		}
	}
	return string(normalized), err
}

const maxTogetherLogprobTokens = 1 << 20

func normalizeTogetherChatPayload(payload []byte, messageField string) ([]byte, error) {
	normalized, err := normalizeChatReasoningAliasPayload("Together", payload, messageField)
	if err != nil {
		return nil, err
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(normalized, &envelope); err != nil {
		return nil, err
	}
	var choices []map[string]json.RawMessage
	if raw := envelope["choices"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &choices); err != nil {
			return nil, err
		}
	}
	for _, choice := range choices {
		if err := rejectTogetherTopLogprobs(choice["top_logprobs"]); err != nil {
			return nil, err
		}
		raw := choice["logprobs"]
		if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) == nil {
			if object["content"] != nil || object["refusal"] != nil {
				continue
			}
			converted, err := togetherLegacyChoiceLogprobs(object)
			if err != nil {
				return nil, err
			}
			choice["logprobs"], _ = json.Marshal(converted)
			continue
		}
		if messageField != "delta" {
			return nil, errors.New("provider returned invalid Together logprobs")
		}
		var probability float64
		if json.Unmarshal(raw, &probability) != nil || !validTogetherLogprob(probability) {
			return nil, errors.New("provider returned invalid Together stream logprob")
		}
		var delta map[string]json.RawMessage
		if json.Unmarshal(choice[messageField], &delta) != nil {
			return nil, errors.New("provider returned Together stream logprob without a delta")
		}
		var token string
		if json.Unmarshal(delta["content"], &token) != nil || token == "" {
			return nil, errors.New("provider returned Together stream logprob without token text")
		}
		if rawID := delta["token_id"]; len(rawID) > 0 {
			var tokenID int64
			if json.Unmarshal(rawID, &tokenID) != nil || tokenID < 0 {
				return nil, errors.New("provider returned invalid Together stream token ID")
			}
		}
		choice["logprobs"], _ = json.Marshal(openai.ChoiceLogprobs{Content: []openai.TokenLogprob{{Token: token, Logprob: probability, Bytes: tokenBytes(token), TopLogprobs: []openai.TopLogprob{}}}})
	}
	envelope["choices"], _ = json.Marshal(choices)
	return json.Marshal(envelope)
}

func togetherLegacyChoiceLogprobs(object map[string]json.RawMessage) (openai.ChoiceLogprobs, error) {
	var tokens []*string
	var probabilities []*float64
	var tokenIDs []*int64
	if json.Unmarshal(object["tokens"], &tokens) != nil || json.Unmarshal(object["token_logprobs"], &probabilities) != nil || len(tokens) == 0 || len(tokens) != len(probabilities) || len(tokens) > maxTogetherLogprobTokens {
		return openai.ChoiceLogprobs{}, errors.New("provider returned inconsistent Together logprobs")
	}
	if raw := object["token_ids"]; len(raw) > 0 {
		if json.Unmarshal(raw, &tokenIDs) != nil || len(tokenIDs) != len(tokens) {
			return openai.ChoiceLogprobs{}, errors.New("provider returned inconsistent Together token IDs")
		}
	}
	if err := rejectTogetherTopLogprobs(object["top_logprobs"]); err != nil {
		return openai.ChoiceLogprobs{}, err
	}
	result := openai.ChoiceLogprobs{Content: make([]openai.TokenLogprob, len(tokens))}
	for index := range tokens {
		if tokens[index] == nil || *tokens[index] == "" || probabilities[index] == nil || !validTogetherLogprob(*probabilities[index]) {
			return openai.ChoiceLogprobs{}, errors.New("provider returned invalid Together logprob token")
		}
		if len(tokenIDs) > 0 && tokenIDs[index] != nil && *tokenIDs[index] < 0 {
			return openai.ChoiceLogprobs{}, errors.New("provider returned invalid Together token ID")
		}
		result.Content[index] = openai.TokenLogprob{Token: *tokens[index], Logprob: *probabilities[index], Bytes: tokenBytes(*tokens[index]), TopLogprobs: []openai.TopLogprob{}}
	}
	return result, nil
}

func rejectTogetherTopLogprobs(raw json.RawMessage) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var values map[string]float64
	if json.Unmarshal(raw, &values) == nil && len(values) == 0 {
		return nil
	}
	return errors.New("provider returned positional-ambiguous Together top logprobs")
}

func validTogetherLogprob(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value <= 0
}

func validateTogetherRequestedLogprobs(request openai.ChatCompletionRequest, response openai.ChatCompletionResponse) error {
	if request.Logprobs == nil || !*request.Logprobs {
		return nil
	}
	for _, choice := range response.Choices {
		text := openai.ContentText(choice.Message.Content)
		if text == "" {
			continue
		}
		if choice.Logprobs == nil || len(choice.Logprobs.Content) == 0 || len(choice.Logprobs.Content) > maxTogetherLogprobTokens {
			return errors.New("provider omitted requested Together logprobs")
		}
		var rebuilt strings.Builder
		for _, token := range choice.Logprobs.Content {
			if token.Token == "" || !validTogetherLogprob(token.Logprob) || len(token.TopLogprobs) != 0 {
				return errors.New("provider returned invalid Together logprobs")
			}
			wantBytes := tokenBytes(token.Token)
			if len(token.Bytes) != len(wantBytes) {
				return errors.New("provider returned invalid Together logprob bytes")
			}
			for index := range wantBytes {
				if token.Bytes[index] != wantBytes[index] {
					return errors.New("provider returned invalid Together logprob bytes")
				}
			}
			rebuilt.WriteString(token.Token)
		}
		if rebuilt.String() != text {
			return errors.New("provider returned Together logprobs inconsistent with output text")
		}
	}
	return nil
}

func (t Together) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if err := t.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	normalizeStream := normalizeTogetherChatStreamPayload
	if request.Logprobs != nil && *request.Logprobs {
		normalizeStream = func(payload string) (string, error) { return normalizeTogetherChatStreamPayloadRequired(payload, true) }
	}
	response, err := t.compatible.chatCompletions(ctx, request, decodeTogetherChatCompletionResponse, normalizeStream)
	if err == nil {
		err = validateTogetherRequestedLogprobs(request, response)
	}
	return response, err
}

func (t Together) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	if err := t.ValidateChatParameters(request); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	normalizeStream := normalizeTogetherChatStreamPayload
	if request.Logprobs != nil && *request.Logprobs {
		normalizeStream = func(payload string) (string, error) { return normalizeTogetherChatStreamPayloadRequired(payload, true) }
	}
	response, err := t.compatible.streamChatCompletions(ctx, request, write, normalizeStream)
	if err == nil {
		err = validateTogetherRequestedLogprobs(request, response)
	}
	return response, err
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

const togetherMaxAudioMilliseconds = 4 * 60 * 60 * 1000

func (Together) ReserveAudioMilliseconds(request openai.AudioTranscriptionRequest) (int, error) {
	data, err := base64.StdEncoding.DecodeString(request.File.Data)
	if err != nil {
		return 0, openai.ErrInvalidAudio
	}
	switch strings.ToLower(request.File.MediaType) {
	case "audio/wav", "audio/wave", "audio/x-wav":
		return togetherBoundedAudioDuration(wavDurationMilliseconds(data))
	case "audio/flac":
		return togetherBoundedAudioDuration(flacDurationMilliseconds(data))
	case "audio/ogg", "audio/opus":
		return togetherBoundedAudioDuration(oggDurationMilliseconds(data))
	case "audio/mpeg", "audio/mp3":
		return togetherBoundedAudioDuration(mp3DurationMilliseconds(data))
	case "audio/mp4", "video/mp4", "audio/x-m4a", "audio/m4a":
		return togetherBoundedAudioDuration(mp4DurationMilliseconds(data))
	case "audio/webm", "video/webm":
		return togetherBoundedAudioDuration(webmAudioDurationMilliseconds(data))
	default:
		return 0, errors.New("together transcription requires audio with reliable duration metadata")
	}
}

func (t Together) ReserveTranslationAudioMilliseconds(request openai.AudioTranscriptionRequest) (int, error) {
	return t.ReserveAudioMilliseconds(request)
}

func togetherBoundedAudioDuration(duration int, err error) (int, error) {
	if err != nil || duration <= 0 || duration > togetherMaxAudioMilliseconds {
		return 0, errors.New("together transcription duration is invalid or exceeds four hours")
	}
	return duration, nil
}

func (Together) ValidateAudioTranscriptionParameters(request openai.AudioTranscriptionRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "together", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	invalidLanguage := request.Language != "" && request.Language != "auto" && (len(request.Language) != 2 || request.Language != strings.ToLower(request.Language))
	return rejectParameters("together",
		parameterCheck{"language", invalidLanguage},
		parameterCheck{"prompt", request.Prompt != ""},
		parameterCheck{"response_format", request.ResponseFormat == "diarized_json"},
		parameterCheck{"include", len(request.Include) > 0},
		parameterCheck{"languages", len(request.Languages) > 0},
		parameterCheck{"keywords", len(request.Keywords) > 0},
		parameterCheck{"mode", request.Mode != ""},
		parameterCheck{"chunking_strategy", request.ChunkingStrategy != nil},
		parameterCheck{"known_speaker_names", len(request.KnownSpeakerNames) > 0},
		parameterCheck{"known_speaker_references", len(request.KnownSpeakerReferences) > 0},
		parameterCheck{"stream", request.Stream},
	)
}

func (t Together) TranscribeAudio(ctx context.Context, request openai.AudioTranscriptionRequest) (openai.AudioTranscriptionResponse, error) {
	if err := t.ValidateAudioTranscriptionParameters(request); err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	duration, err := t.ReserveAudioMilliseconds(request)
	if err != nil {
		return openai.AudioTranscriptionResponse{}, &Error{Class: FailureClientRequest, Provider: "together", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_audio", Param: "file", Err: err}
	}
	return t.sendAudioRequest(ctx, request, "audio/transcriptions", duration, true)
}

func (Together) ValidateAudioTranslationParameters(request openai.AudioTranscriptionRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "together", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	return rejectParameters("together",
		parameterCheck{"language", request.Language != ""},
		parameterCheck{"timestamp_granularities", len(request.TimestampGranularities) > 0},
		parameterCheck{"response_format", request.ResponseFormat == "diarized_json"},
		parameterCheck{"include", len(request.Include) > 0},
		parameterCheck{"languages", len(request.Languages) > 0},
		parameterCheck{"keywords", len(request.Keywords) > 0},
		parameterCheck{"mode", request.Mode != ""},
		parameterCheck{"chunking_strategy", request.ChunkingStrategy != nil},
		parameterCheck{"known_speaker_names", len(request.KnownSpeakerNames) > 0},
		parameterCheck{"known_speaker_references", len(request.KnownSpeakerReferences) > 0},
		parameterCheck{"stream", request.Stream},
	)
}

func (t Together) TranslateAudio(ctx context.Context, request openai.AudioTranscriptionRequest) (openai.AudioTranscriptionResponse, error) {
	if err := t.ValidateAudioTranslationParameters(request); err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	duration, err := t.ReserveTranslationAudioMilliseconds(request)
	if err != nil {
		return openai.AudioTranscriptionResponse{}, &Error{Class: FailureClientRequest, Provider: "together", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_audio", Param: "file", Err: err}
	}
	return t.sendAudioRequest(ctx, request, "audio/translations", duration, false)
}

func (t Together) sendAudioRequest(ctx context.Context, request openai.AudioTranscriptionRequest, path string, duration int, includeTranscriptionOptions bool) (openai.AudioTranscriptionResponse, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := map[string]string{"model": request.Model, "prompt": request.Prompt, "response_format": request.ResponseFormat}
	if includeTranscriptionOptions {
		fields["language"] = request.Language
	}
	if request.Temperature != nil {
		fields["temperature"] = strconv.FormatFloat(*request.Temperature, 'g', -1, 64)
	}
	for name, value := range fields {
		if value != "" {
			if err := writer.WriteField(name, value); err != nil {
				return openai.AudioTranscriptionResponse{}, err
			}
		}
	}
	if includeTranscriptionOptions {
		for _, value := range request.TimestampGranularities {
			if err := writer.WriteField("timestamp_granularities[]", value); err != nil {
				return openai.AudioTranscriptionResponse{}, err
			}
		}
	}
	if err := writeAudioPart(writer, request.File); err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	if err := writer.Close(); err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(t.compatible.baseURL, path), &body)
	if err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", writer.FormDataContentType())
	if t.compatible.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+t.compatible.apiKey)
	}
	response, err := t.compatible.client.Do(httpRequest)
	if err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.AudioTranscriptionResponse{}, responseStatusError("together", response)
	}
	return decodeTogetherAudioResponse(response.Body, duration)
}

func decodeTogetherAudioResponse(reader io.Reader, duration int) (openai.AudioTranscriptionResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxAudioTranscriptionResponseBytes+1))
	if err != nil || len(payload) > maxAudioTranscriptionResponseBytes {
		return openai.AudioTranscriptionResponse{}, errors.New("together audio response exceeds limit")
	}
	var response *openai.AudioTranscriptionResponse
	if err := json.Unmarshal(payload, &response); err != nil || response == nil {
		return openai.AudioTranscriptionResponse{}, errors.New("together audio response must be an object")
	}
	response.Duration = float64(duration) / 1000
	response.Usage = &openai.AudioTranscriptionUsage{Type: "duration", InputAudioMilliseconds: duration}
	if message := response.Validate(); message != "" {
		return openai.AudioTranscriptionResponse{}, errors.New(message)
	}
	return *response, nil
}

func (Together) ValidateRerankParameters(request openai.RerankRequest) error {
	return rejectParameters("together",
		parameterCheck{"rank_fields", len(request.RankFields) > 0},
		parameterCheck{"max_chunks_per_doc", request.MaxChunksPerDoc != nil},
		parameterCheck{"max_tokens_per_doc", request.MaxTokensPerDoc != nil},
		parameterCheck{"truncate", request.Truncate != ""},
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
