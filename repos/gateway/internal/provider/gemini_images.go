package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ai-gateway-gateway/internal/openai"
)

func (Gemini) SupportsImageGeneration() bool { return true }
func (Gemini) SupportsImageEdit() bool       { return true }
func (Gemini) SupportsImageVariation() bool  { return true }

func (Gemini) ValidateImageGenerationParameters(request openai.ImageGenerationRequest) error {
	return validateGeminiImageRequest(request)
}

func (Gemini) ValidateImageEditParameters(request openai.ImageEditRequest) error {
	return validateGeminiImageEditRequest(request)
}

func (g Gemini) GenerateImage(ctx context.Context, request openai.ImageGenerationRequest) (openai.ImageGenerationResponse, error) {
	if err := validateGeminiImageRequest(request); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	imageConfig := &geminiImageConfig{}
	if request.AspectRatio != "auto" {
		imageConfig.AspectRatio = request.AspectRatio
	}
	imageConfig.ImageSize = request.Resolution
	if imageConfig.AspectRatio == "" && imageConfig.ImageSize == "" {
		imageConfig = nil
	}
	body := geminiRequest{
		Contents: []geminiContent{{Parts: []geminiPart{{Text: request.Prompt}}}},
		Generation: geminiGeneration{
			ResponseModalities: []string{"IMAGE"},
			ImageConfig:        imageConfig,
		},
	}
	return g.executeImageRequest(ctx, request.Model, body, request)
}

func (g Gemini) EditImage(ctx context.Context, request openai.ImageEditRequest) (openai.ImageGenerationResponse, error) {
	if err := validateGeminiImageEditRequest(request); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	parts := make([]geminiPart, 0, len(request.Images)+1)
	parts = append(parts, geminiPart{Text: request.Prompt})
	for _, image := range request.Images {
		parts = append(parts, geminiPart{InlineData: &geminiInlineData{MIMEType: image.MediaType, Data: image.Data}})
	}
	body := geminiRequest{
		Contents:   []geminiContent{{Parts: parts}},
		Generation: geminiGeneration{ResponseModalities: []string{"IMAGE"}},
	}
	return g.executeImageRequest(ctx, request.Model, body, request.GenerationRequest())
}

func (g Gemini) CreateImageVariation(ctx context.Context, request openai.ImageVariationRequest) (openai.ImageGenerationResponse, error) {
	if err := g.ValidateImageVariationParameters(request); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	body := geminiRequest{
		Contents: []geminiContent{{Parts: []geminiPart{
			{Text: "Create a distinct variation of the provided image while preserving its main subject."},
			{InlineData: &geminiInlineData{MIMEType: request.Image.MediaType, Data: request.Image.Data}},
		}}},
		Generation: geminiGeneration{ResponseModalities: []string{"IMAGE"}},
	}
	return g.executeImageRequest(ctx, request.Model, body, request.GenerationRequest())
}

func (Gemini) ValidateImageVariationParameters(request openai.ImageVariationRequest) error {
	return validateGeminiImageVariationRequest(request)
}

func (g Gemini) executeImageRequest(ctx context.Context, modelName string, body geminiRequest, responseRequest openai.ImageGenerationRequest) (openai.ImageGenerationResponse, error) {
	model := strings.TrimPrefix(modelName, "models/")
	if model == "" || strings.ContainsAny(model, "/\\?#%") || model == "." || model == ".." {
		return openai.ImageGenerationResponse{}, geminiInvalid("model")
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	base, err := url.Parse(g.baseURL)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return openai.ImageGenerationResponse{}, errors.New("invalid Gemini base URL")
	}
	endpoint := geminiBaseURL(g.baseURL) + "/models/" + url.PathEscape(model) + ":generateContent"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if err := g.authorize(httpRequest); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	response, err := g.client.Do(httpRequest)
	if err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.ImageGenerationResponse{}, responseStatusError("gemini", response)
	}
	return decodeGeminiImageResponse(response.Body, responseRequest)
}

func validateGeminiImageEditRequest(request openai.ImageEditRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "gemini", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	if request.Mask != nil {
		return geminiInvalid("mask")
	}
	for _, image := range request.Images {
		if !oneOfOrEmptyImageValue(image.MediaType, "image/png", "image/jpeg", "image/webp") {
			return geminiInvalid("image")
		}
	}
	if request.N != nil && *request.N != 1 {
		return geminiInvalid("n")
	}
	if request.ResponseFormat != "" && request.ResponseFormat != "b64_json" {
		return geminiInvalid("response_format")
	}
	for _, unsupported := range []struct {
		name string
		set  bool
	}{
		{"quality", request.Quality != ""},
		{"size", request.Size != ""},
		{"user", request.User != ""},
		{"background", request.Background != ""},
		{"output_format", request.OutputFormat != ""},
		{"output_compression", request.OutputCompression != nil},
		{"stream", request.Stream},
		{"partial_images", request.PartialImages != nil},
	} {
		if unsupported.set {
			return geminiInvalid(unsupported.name)
		}
	}
	return nil
}

func validateGeminiImageVariationRequest(request openai.ImageVariationRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "gemini", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	if !oneOfOrEmptyImageValue(request.Image.MediaType, "image/png", "image/jpeg", "image/webp") {
		return geminiInvalid("image")
	}
	if request.N != nil && *request.N != 1 {
		return geminiInvalid("n")
	}
	if request.ResponseFormat != "" && request.ResponseFormat != "b64_json" {
		return geminiInvalid("response_format")
	}
	if request.Size != "" {
		return geminiInvalid("size")
	}
	if request.User != "" {
		return geminiInvalid("user")
	}
	return nil
}

func validateGeminiImageRequest(request openai.ImageGenerationRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "gemini", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	if request.N != nil && *request.N != 1 {
		return geminiInvalid("n")
	}
	if request.ResponseFormat != "" && request.ResponseFormat != "b64_json" {
		return geminiInvalid("response_format")
	}
	for _, unsupported := range []struct {
		name string
		set  bool
	}{
		{"quality", request.Quality != ""},
		{"size", request.Size != ""},
		{"style", request.Style != ""},
		{"user", request.User != ""},
		{"background", request.Background != ""},
		{"output_format", request.OutputFormat != ""},
		{"output_compression", request.OutputCompression != nil},
		{"seed", request.Seed != nil},
		{"stream", request.Stream},
		{"partial_images", request.PartialImages != nil},
	} {
		if unsupported.set {
			return geminiInvalid(unsupported.name)
		}
	}
	if request.AspectRatio != "" && request.AspectRatio != "auto" && !oneOfOrEmptyImageValue(request.AspectRatio, "1:1", "1:4", "4:1", "1:8", "8:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9") {
		return geminiInvalid("aspect_ratio")
	}
	return nil
}

func decodeGeminiImageResponse(body io.Reader, request openai.ImageGenerationRequest) (openai.ImageGenerationResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(body, maxImageGenerationResponseBytes+1))
	if err != nil || len(payload) > maxImageGenerationResponseBytes {
		return openai.ImageGenerationResponse{}, errors.New("Gemini image response exceeds limit")
	}
	var upstream geminiResponse
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&upstream); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return openai.ImageGenerationResponse{}, errors.New("invalid trailing Gemini image response data")
	}
	if upstream.Error != nil {
		code := upstream.Error.Code
		if code < 400 || code > 599 {
			code = http.StatusBadGateway
		}
		return openai.ImageGenerationResponse{}, statusError("gemini", code)
	}
	if upstream.PromptFeedback.BlockReason != "" {
		return openai.ImageGenerationResponse{}, &Error{Class: FailureContentPolicy, Provider: "gemini", StatusCode: http.StatusBadRequest, UpstreamCode: "content_policy_violation", Err: errors.New("upstream content policy rejected prompt")}
	}
	if len(upstream.Candidates) != 1 || upstream.Usage == nil || upstream.Usage.Prompt < 0 || upstream.Usage.Cached < 0 || upstream.Usage.Cached > upstream.Usage.Prompt || upstream.Usage.Candidates < 0 || upstream.Usage.Thoughts < 0 || upstream.Usage.Candidates > int(^uint(0)>>1)-upstream.Usage.Thoughts || upstream.Usage.Total < 0 {
		return openai.ImageGenerationResponse{}, errors.New("Gemini returned invalid image candidates or usage")
	}
	completionTokens := upstream.Usage.Candidates + upstream.Usage.Thoughts
	if upstream.Usage.Prompt > int(^uint(0)>>1)-completionTokens || upstream.Usage.Total < upstream.Usage.Prompt+completionTokens {
		return openai.ImageGenerationResponse{}, errors.New("Gemini returned inconsistent image usage")
	}
	result := openai.ImageGenerationResponse{
		Created: time.Now().Unix(),
		Usage: &openai.ImageUsage{
			InputTokens:  upstream.Usage.Prompt,
			OutputTokens: upstream.Usage.Total - upstream.Usage.Prompt,
			TotalTokens:  upstream.Usage.Total,
		},
	}
	parts := upstream.Candidates[0].Content.Parts
	if len(parts) != 1 || parts[0].InlineData == nil || parts[0].Text != "" || parts[0].FunctionCall != nil || parts[0].FunctionResponse != nil || parts[0].Thought {
		return openai.ImageGenerationResponse{}, errors.New("Gemini returned an invalid image count")
	}
	result.Data = []openai.ImageData{{B64JSON: parts[0].InlineData.Data, MediaType: parts[0].InlineData.MIMEType}}
	if err := validateImageGenerationResponse(result, request); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	return result, nil
}
