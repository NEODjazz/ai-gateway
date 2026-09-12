package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"ai-gateway-gateway/internal/openai"
)

// OpenRouter exposes only operations whose wire contracts are implemented by
// this adapter. Keeping the compatible client private prevents unrelated
// compatible endpoints from being advertised by interface discovery.
type OpenRouter struct {
	compatible OpenAICompatible
}

func NewOpenRouter(baseURL, apiKey string, stream bool, rerankPath string) OpenRouter {
	compatible := NewOpenAICompatibleWithRerankPath(baseURL, apiKey, stream, rerankPath)
	compatible.errorProvider = "openrouter"
	return OpenRouter{compatible: compatible}
}

func (OpenRouter) SupportsResponses() bool        { return true }
func (OpenRouter) SupportsTools() bool            { return true }
func (OpenRouter) SupportsStructuredOutput() bool { return true }
func (OpenRouter) SupportsVision() bool           { return true }
func (OpenRouter) SupportsWebSearch() bool        { return true }
func (OpenRouter) SupportsChatAudio() bool        { return true }

func (p OpenRouter) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	return p.compatible.ValidateChatParameters(request)
}

func (p OpenRouter) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return p.compatible.ChatCompletions(ctx, request)
}

func (p OpenRouter) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	return p.compatible.StreamChatCompletions(ctx, request, write)
}

func (p OpenRouter) ValidateResponseParameters(request openai.ResponseRequest) error {
	return p.compatible.ValidateResponseParameters(request)
}

func (p OpenRouter) Responses(ctx context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	return p.compatible.Responses(ctx, request)
}

func (p OpenRouter) StreamResponses(ctx context.Context, request openai.ResponseRequest, write ResponseStreamWriter) (openai.ResponseResponse, error) {
	return p.compatible.StreamResponses(ctx, request, write)
}

func (p OpenRouter) ValidateCompletionParameters(request openai.CompletionRequest) error {
	return p.compatible.ValidateCompletionParameters(request)
}

func (p OpenRouter) Completions(ctx context.Context, request openai.CompletionRequest) (openai.CompletionResponse, error) {
	return p.compatible.Completions(ctx, request)
}

func (p OpenRouter) StreamCompletions(ctx context.Context, request openai.CompletionRequest, write CompletionStreamWriter) (openai.CompletionResponse, error) {
	return p.compatible.StreamCompletions(ctx, request, write)
}

func (p OpenRouter) ValidateEmbeddingParameters(request openai.EmbeddingRequest) error {
	return p.compatible.ValidateEmbeddingParameters(request)
}

func (p OpenRouter) Embeddings(ctx context.Context, request openai.EmbeddingRequest) (openai.EmbeddingResponse, error) {
	return p.compatible.Embeddings(ctx, request)
}

func (p OpenRouter) Rerank(ctx context.Context, request openai.RerankRequest) (openai.RerankResponse, error) {
	if err := p.ValidateRerankParameters(request); err != nil {
		return openai.RerankResponse{}, err
	}
	return p.compatible.Rerank(ctx, request)
}

func (OpenRouter) ValidateRerankParameters(request openai.RerankRequest) error {
	return rejectParameters("openrouter",
		parameterCheck{"rank_fields", len(request.RankFields) > 0},
		parameterCheck{"max_chunks_per_doc", request.MaxChunksPerDoc != nil},
		parameterCheck{"max_tokens_per_doc", request.MaxTokensPerDoc != nil},
	)
}

func (p OpenRouter) TranscribeAudio(ctx context.Context, request openai.AudioTranscriptionRequest) (openai.AudioTranscriptionResponse, error) {
	if err := p.ValidateAudioTranscriptionParameters(request); err != nil {
		return openai.AudioTranscriptionResponse{}, err
	}
	return p.compatible.TranscribeAudio(ctx, request)
}

func (p OpenRouter) ValidateAudioTranscriptionParameters(request openai.AudioTranscriptionRequest) error {
	if err := p.compatible.ValidateAudioTranscriptionParameters(request); err != nil {
		return err
	}
	return rejectParameters("openrouter",
		parameterCheck{"include", len(request.Include) > 0}, parameterCheck{"languages", len(request.Languages) > 0},
		parameterCheck{"keywords", len(request.Keywords) > 0}, parameterCheck{"chunking_strategy", request.ChunkingStrategy != nil},
		parameterCheck{"known_speaker_names", len(request.KnownSpeakerNames) > 0},
		parameterCheck{"known_speaker_references", len(request.KnownSpeakerReferences) > 0},
	)
}

func (p OpenRouter) GenerateSpeech(ctx context.Context, request openai.AudioSpeechRequest) (openai.AudioSpeechResponse, error) {
	if err := p.ValidateAudioSpeechParameters(request); err != nil {
		return openai.AudioSpeechResponse{}, err
	}
	return p.compatible.GenerateSpeech(ctx, request)
}

func (p OpenRouter) ValidateAudioSpeechParameters(request openai.AudioSpeechRequest) error {
	if err := p.compatible.ValidateAudioSpeechParameters(request); err != nil {
		return err
	}
	return rejectParameters("openrouter",
		parameterCheck{"instructions", request.Instructions != ""}, parameterCheck{"stream_format", request.StreamFormat != ""},
	)
}

func (p OpenRouter) GenerateImage(ctx context.Context, request openai.ImageGenerationRequest) (openai.ImageGenerationResponse, error) {
	return p.generateImage(ctx, request, nil)
}

func (OpenRouter) ValidateImageGenerationParameters(request openai.ImageGenerationRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "openrouter", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	return rejectParameters("openrouter", parameterCheck{"response_format", request.ResponseFormat != ""}, parameterCheck{"style", request.Style != ""}, parameterCheck{"stream", request.Stream}, parameterCheck{"partial_images", request.PartialImages != nil})
}

func (p OpenRouter) EditImage(ctx context.Context, request openai.ImageEditRequest) (openai.ImageGenerationResponse, error) {
	if err := p.ValidateImageEditParameters(request); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	references := make([]openRouterImageReference, len(request.Images))
	for index, image := range request.Images {
		references[index] = openRouterImageReference{Type: "image_url", ImageURL: struct {
			URL string `json:"url"`
		}{URL: "data:" + image.MediaType + ";base64," + image.Data}}
	}
	return p.generateImage(ctx, request.GenerationRequest(), references)
}

func (p OpenRouter) ValidateImageEditParameters(request openai.ImageEditRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "openrouter", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	if err := rejectParameters("openrouter", parameterCheck{"mask", request.Mask != nil}, parameterCheck{"response_format", request.ResponseFormat != ""}, parameterCheck{"stream", request.Stream}, parameterCheck{"partial_images", request.PartialImages != nil}); err != nil {
		return err
	}
	return p.ValidateImageGenerationParameters(request.GenerationRequest())
}

type openRouterImageReference struct {
	Type     string `json:"type"`
	ImageURL struct {
		URL string `json:"url"`
	} `json:"image_url"`
}

func (p OpenRouter) generateImage(ctx context.Context, request openai.ImageGenerationRequest, references []openRouterImageReference) (openai.ImageGenerationResponse, error) {
	if err := p.ValidateImageGenerationParameters(request); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	body, err := json.Marshal(struct {
		Model             string                     `json:"model"`
		Prompt            string                     `json:"prompt"`
		N                 *int                       `json:"n,omitempty"`
		Quality           string                     `json:"quality,omitempty"`
		Size              string                     `json:"size,omitempty"`
		Background        string                     `json:"background,omitempty"`
		OutputFormat      string                     `json:"output_format,omitempty"`
		OutputCompression *int                       `json:"output_compression,omitempty"`
		User              string                     `json:"user,omitempty"`
		Resolution        string                     `json:"resolution,omitempty"`
		AspectRatio       string                     `json:"aspect_ratio,omitempty"`
		Seed              *int64                     `json:"seed,omitempty"`
		InputReferences   []openRouterImageReference `json:"input_references,omitempty"`
	}{request.Model, request.Prompt, request.N, request.Quality, request.Size, request.Background, request.OutputFormat, request.OutputCompression, request.User, request.Resolution, request.AspectRatio, request.Seed, references})
	if err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.compatible.baseURL, "images"), bytes.NewReader(body))
	if err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if p.compatible.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+p.compatible.apiKey)
	}
	response, err := p.compatible.client.Do(httpRequest)
	if err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.ImageGenerationResponse{}, responseStatusError("openrouter", response)
	}
	return decodeOpenRouterImageResponse(response.Body, request)
}

func decodeOpenRouterImageResponse(reader io.Reader, request openai.ImageGenerationRequest) (openai.ImageGenerationResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxImageGenerationResponseBytes+1))
	if err != nil || len(payload) > maxImageGenerationResponseBytes {
		return openai.ImageGenerationResponse{}, errors.New("image generation response exceeds limit")
	}
	var wire struct {
		Created      int64              `json:"created"`
		Data         []openai.ImageData `json:"data"`
		Background   string             `json:"background,omitempty"`
		OutputFormat string             `json:"output_format,omitempty"`
		Quality      string             `json:"quality,omitempty"`
		Size         string             `json:"size,omitempty"`
		Usage        *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&wire); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return openai.ImageGenerationResponse{}, errors.New("invalid trailing image response data")
	}
	result := openai.ImageGenerationResponse{Created: wire.Created, Data: wire.Data, Background: wire.Background, OutputFormat: wire.OutputFormat, Quality: wire.Quality, Size: wire.Size}
	if wire.Usage != nil {
		result.Usage = &openai.ImageUsage{InputTokens: wire.Usage.PromptTokens, OutputTokens: wire.Usage.CompletionTokens, TotalTokens: wire.Usage.TotalTokens}
	}
	if err := validateImageGenerationResponseCount(result, request, false); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	return result, nil
}
