package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"ai-gateway-gateway/internal/openai"
)

const maxImageGenerationResponseBytes = 64 << 20
const maxGeneratedImageBytes = 20 << 20
const maxImageGenerationStreamBytes = 96 << 20

func (OpenAICompatible) SupportsImageGeneration() bool { return true }

func (p OpenAICompatible) ValidateImageGenerationParameters(request openai.ImageGenerationRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: p.providerName(), StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	return nil
}

func (p OpenAICompatible) GenerateImage(ctx context.Context, request openai.ImageGenerationRequest) (openai.ImageGenerationResponse, error) {
	if err := p.ValidateImageGenerationParameters(request); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	body, err := json.Marshal(struct {
		Model             string `json:"model"`
		Prompt            string `json:"prompt"`
		N                 *int   `json:"n,omitempty"`
		Quality           string `json:"quality,omitempty"`
		ResponseFormat    string `json:"response_format,omitempty"`
		Size              string `json:"size,omitempty"`
		Style             string `json:"style,omitempty"`
		User              string `json:"user,omitempty"`
		Background        string `json:"background,omitempty"`
		OutputFormat      string `json:"output_format,omitempty"`
		OutputCompression *int   `json:"output_compression,omitempty"`
		Resolution        string `json:"resolution,omitempty"`
		AspectRatio       string `json:"aspect_ratio,omitempty"`
		Seed              *int64 `json:"seed,omitempty"`
	}{request.Model, request.Prompt, request.N, request.Quality, request.ResponseFormat, request.Size, request.Style, request.User, request.Background, request.OutputFormat, request.OutputCompression, request.Resolution, request.AspectRatio, request.Seed})
	if err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	return p.postImageJSON(ctx, "images/generations", body, request, false)
}

func (p OpenAICompatible) StreamGenerateImage(ctx context.Context, request openai.ImageGenerationRequest, write ImageGenerationStreamWriter) (openai.ImageGenerationResponse, error) {
	if err := p.ValidateImageGenerationParameters(request); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	if !p.upstreamStream {
		return openai.ImageGenerationResponse{}, ErrStreamingUnsupported
	}
	body, err := json.Marshal(struct {
		Model             string `json:"model"`
		Prompt            string `json:"prompt"`
		N                 *int   `json:"n,omitempty"`
		Quality           string `json:"quality,omitempty"`
		ResponseFormat    string `json:"response_format,omitempty"`
		Size              string `json:"size,omitempty"`
		Style             string `json:"style,omitempty"`
		User              string `json:"user,omitempty"`
		Background        string `json:"background,omitempty"`
		OutputFormat      string `json:"output_format,omitempty"`
		OutputCompression *int   `json:"output_compression,omitempty"`
		Resolution        string `json:"resolution,omitempty"`
		AspectRatio       string `json:"aspect_ratio,omitempty"`
		Seed              *int64 `json:"seed,omitempty"`
		Stream            bool   `json:"stream"`
		PartialImages     *int   `json:"partial_images,omitempty"`
	}{request.Model, request.Prompt, request.N, request.Quality, request.ResponseFormat, request.Size, request.Style, request.User, request.Background, request.OutputFormat, request.OutputCompression, request.Resolution, request.AspectRatio, request.Seed, true, request.PartialImages})
	if err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "images/generations"), bytes.NewReader(body))
	if err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	response, err := p.client.Do(httpReq)
	if err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.ImageGenerationResponse{}, responseStatusError(p.providerName(), response)
	}
	if mediaType := response.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(mediaType), "text/event-stream") {
		return openai.ImageGenerationResponse{}, errors.New("provider returned a non-SSE image stream")
	}
	return streamImageGeneration(&responseStreamReader{source: response.Body, remaining: maxImageGenerationStreamBytes}, request, write)
}

type imageGenerationStreamEvent struct {
	Type              string             `json:"type"`
	B64JSON           string             `json:"b64_json"`
	Background        string             `json:"background"`
	CreatedAt         int64              `json:"created_at"`
	OutputFormat      string             `json:"output_format"`
	PartialImageIndex *int               `json:"partial_image_index,omitempty"`
	Quality           string             `json:"quality"`
	Size              string             `json:"size"`
	Usage             *openai.ImageUsage `json:"usage,omitempty"`
}

func streamImageGeneration(body io.Reader, request openai.ImageGenerationRequest, write ImageGenerationStreamWriter) (openai.ImageGenerationResponse, error) {
	var result openai.ImageGenerationResponse
	completed := false
	partialCount := 0
	err := scanSSEData(body, func(payload string) error {
		if completed {
			return errors.New("provider returned image events after completion")
		}
		var event imageGenerationStreamEvent
		decoder := json.NewDecoder(strings.NewReader(payload))
		if err := decoder.Decode(&event); err != nil {
			return errors.New("provider returned an invalid image stream event")
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			return errors.New("provider returned trailing image stream data")
		}
		image := openai.ImageGenerationResponse{
			Created: event.CreatedAt, Background: event.Background, OutputFormat: event.OutputFormat,
			Quality: event.Quality, Size: event.Size, Data: []openai.ImageData{{B64JSON: event.B64JSON, MediaType: imageMediaType(event.OutputFormat)}}, Usage: event.Usage,
		}
		switch event.Type {
		case "image_generation.partial_image":
			if event.PartialImageIndex == nil || *event.PartialImageIndex != partialCount || request.PartialImages == nil || partialCount >= *request.PartialImages {
				return errors.New("provider returned an invalid partial image index")
			}
			image.Usage = &openai.ImageUsage{}
			if err := validateImageGenerationResponseCount(image, request, false); err != nil {
				return err
			}
			partialCount++
			return write(payload)
		case "image_generation.completed":
			if event.PartialImageIndex != nil {
				return errors.New("provider returned a partial index on completion")
			}
			if request.PartialImages != nil && partialCount != *request.PartialImages {
				return errors.New("provider returned an unexpected partial image count")
			}
			if err := validateImageGenerationResponseCount(image, request, true); err != nil {
				return err
			}
			completed = true
			result = image
			return write(payload)
		default:
			return errors.New("provider returned an unsupported image stream event")
		}
	})
	if err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	if !completed {
		return openai.ImageGenerationResponse{}, errors.New("provider image stream ended before completion")
	}
	return result, nil
}

func imageMediaType(format string) string {
	if format == "" {
		return ""
	}
	return "image/" + format
}

func (p OpenAICompatible) postImageJSON(ctx context.Context, path string, body []byte, request openai.ImageGenerationRequest, allowProviderCost bool) (openai.ImageGenerationResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, path), bytes.NewReader(body))
	if err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	response, err := p.client.Do(httpReq)
	if err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.ImageGenerationResponse{}, responseStatusError(p.providerName(), response)
	}
	return decodeImageGenerationResponseWithProviderCost(response.Body, request, allowProviderCost)
}

func decodeImageGenerationResponse(body io.Reader, request openai.ImageGenerationRequest) (openai.ImageGenerationResponse, error) {
	return decodeImageGenerationResponseWithProviderCost(body, request, false)
}

func decodeImageGenerationResponseWithProviderCost(body io.Reader, request openai.ImageGenerationRequest, allowProviderCost bool) (openai.ImageGenerationResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(body, maxImageGenerationResponseBytes+1))
	if err != nil || len(payload) > maxImageGenerationResponseBytes {
		return openai.ImageGenerationResponse{}, errors.New("image generation response exceeds limit")
	}
	var result openai.ImageGenerationResponse
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&result); err != nil {
		return result, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return result, errors.New("invalid trailing image response data")
	}
	if err := validateImageGenerationResponseCountWithProviderCost(result, request, true, allowProviderCost); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	return result, nil
}

func validateImageGenerationResponse(response openai.ImageGenerationResponse, request openai.ImageGenerationRequest) error {
	return validateImageGenerationResponseCount(response, request, true)
}

func validateImageGenerationResponseCount(response openai.ImageGenerationResponse, request openai.ImageGenerationRequest, exactCount bool) error {
	return validateImageGenerationResponseCountWithProviderCost(response, request, exactCount, false)
}

func validateImageGenerationResponseCountWithProviderCost(response openai.ImageGenerationResponse, request openai.ImageGenerationRequest, exactCount, allowProviderCost bool) error {
	if response.Created < 0 || !oneOfOrEmptyImageValue(response.Background, "auto", "transparent", "opaque") || !oneOfOrEmptyImageValue(response.OutputFormat, "png", "webp", "jpeg", "svg") || !oneOfOrEmptyImageValue(response.Quality, "auto", "low", "medium", "high", "xhigh", "max") || !oneOfOrEmptyImageValue(response.Size, "auto", "256x256", "512x512", "1024x1024", "1536x1024", "1024x1536", "1792x1024", "1024x1792") {
		return errors.New("provider returned invalid image metadata")
	}
	want := 1
	if request.N != nil {
		want = *request.N
	}
	if len(response.Data) == 0 || len(response.Data) > want || len(response.Data) > openai.MaxGeneratedImages || (exactCount && len(response.Data) != want) {
		return errors.New("provider returned an invalid image count")
	}
	for _, image := range response.Data {
		if (image.B64JSON == "") == (image.URL == "") || utf8.RuneCountInString(image.RevisedPrompt) > 32000 || !oneOfOrEmptyImageValue(image.MediaType, "image/png", "image/jpeg", "image/webp", "image/svg+xml") {
			return errors.New("provider returned invalid image data")
		}
		if image.B64JSON != "" {
			if base64.StdEncoding.DecodedLen(len(image.B64JSON)) > maxGeneratedImageBytes {
				return errors.New("provider returned an oversized image")
			}
			decoded, err := base64.StdEncoding.DecodeString(image.B64JSON)
			if err != nil || len(decoded) > maxGeneratedImageBytes {
				return errors.New("provider returned invalid base64 image data")
			}
		} else {
			if len(image.URL) > 8192 {
				return errors.New("provider returned an oversized image URL")
			}
			parsed, err := url.Parse(image.URL)
			if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
				return errors.New("provider returned an invalid image URL")
			}
		}
	}
	usage := response.Usage
	if usage == nil {
		return errors.New("provider returned invalid or missing image usage")
	}
	validTokens := usage.InputTokens >= 0 && usage.OutputTokens >= 0 && usage.TotalTokens >= 0 && usage.InputTokens <= int(^uint(0)>>1)-usage.OutputTokens && usage.TotalTokens == usage.InputTokens+usage.OutputTokens
	validProviderCost := usage.ProviderCostUSDTicks != nil && *usage.ProviderCostUSDTicks >= 0
	if !validTokens || allowProviderCost && !validProviderCost {
		return errors.New("provider returned invalid or missing image usage")
	}
	return nil
}

func oneOfOrEmptyImageValue(value string, allowed ...string) bool {
	if value == "" {
		return true
	}
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
