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
	"unicode/utf8"

	"ai-gateway-gateway/internal/openai"
)

const maxImageGenerationResponseBytes = 64 << 20
const maxGeneratedImageBytes = 20 << 20

func (OpenAICompatible) SupportsImageGeneration() bool { return true }

func (p OpenAICompatible) GenerateImage(ctx context.Context, request openai.ImageGenerationRequest) (openai.ImageGenerationResponse, error) {
	if message := request.Validate(); message != "" {
		return openai.ImageGenerationResponse{}, &Error{Class: FailureClientRequest, Provider: p.providerName(), StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
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
	}{request.Model, request.Prompt, request.N, request.Quality, request.ResponseFormat, request.Size, request.Style, request.User, request.Background, request.OutputFormat, request.OutputCompression})
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
	return decodeImageGenerationResponse(response.Body, request)
}

func decodeImageGenerationResponse(body io.Reader, request openai.ImageGenerationRequest) (openai.ImageGenerationResponse, error) {
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
	if err := validateImageGenerationResponse(result, request); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	return result, nil
}

func validateImageGenerationResponse(response openai.ImageGenerationResponse, request openai.ImageGenerationRequest) error {
	return validateImageGenerationResponseCount(response, request, true)
}

func validateImageGenerationResponseCount(response openai.ImageGenerationResponse, request openai.ImageGenerationRequest, exactCount bool) error {
	if response.Created < 0 || !oneOfOrEmptyImageValue(response.Background, "auto", "transparent", "opaque") || !oneOfOrEmptyImageValue(response.OutputFormat, "png", "webp", "jpeg") || !oneOfOrEmptyImageValue(response.Quality, "auto", "low", "medium", "high", "xhigh", "max") || !oneOfOrEmptyImageValue(response.Size, "auto", "256x256", "512x512", "1024x1024", "1536x1024", "1024x1536", "1792x1024", "1024x1792") {
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
		if (image.B64JSON == "") == (image.URL == "") || utf8.RuneCountInString(image.RevisedPrompt) > 32000 {
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
	if usage == nil || usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.TotalTokens < 0 || usage.InputTokens > int(^uint(0)>>1)-usage.OutputTokens || usage.TotalTokens != usage.InputTokens+usage.OutputTokens {
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
