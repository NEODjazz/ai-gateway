package provider

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"strconv"

	"ai-gateway-gateway/internal/openai"
)

func (OpenAICompatible) SupportsImageVariation() bool { return true }

func (p OpenAICompatible) ValidateImageVariationParameters(request openai.ImageVariationRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: p.providerName(), StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	return nil
}

func (p OpenAICompatible) CreateImageVariation(ctx context.Context, request openai.ImageVariationRequest) (openai.ImageGenerationResponse, error) {
	if err := p.ValidateImageVariationParameters(request); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := map[string]string{"model": request.Model, "response_format": request.ResponseFormat, "size": request.Size, "user": request.User}
	if request.N != nil {
		fields["n"] = strconv.Itoa(*request.N)
	}
	for name, value := range fields {
		if value != "" {
			if err := writer.WriteField(name, value); err != nil {
				return openai.ImageGenerationResponse{}, err
			}
		}
	}
	if err := writeImagePart(writer, "image", "image"+imageExtension(request.Image.MediaType), request.Image); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	if err := writer.Close(); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "images/variations"), &body)
	if err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	httpReq.Header.Set("Content-Type", writer.FormDataContentType())
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
	return decodeImageGenerationResponse(response.Body, request.GenerationRequest())
}
