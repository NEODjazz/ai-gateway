package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"strconv"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

func (OpenAICompatible) SupportsImageEdit() bool { return true }

func (p OpenAICompatible) ValidateImageEditParameters(request openai.ImageEditRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: p.providerName(), StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	return nil
}

func (p OpenAICompatible) EditImage(ctx context.Context, request openai.ImageEditRequest) (openai.ImageGenerationResponse, error) {
	if err := p.ValidateImageEditParameters(request); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	if request.Stream {
		return openai.ImageGenerationResponse{}, ErrStreamingUnsupported
	}
	httpReq, err := p.imageEditRequest(ctx, request, false)
	if err != nil {
		return openai.ImageGenerationResponse{}, err
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

func (p OpenAICompatible) StreamEditImage(ctx context.Context, request openai.ImageEditRequest, write ImageGenerationStreamWriter) (openai.ImageGenerationResponse, error) {
	if err := p.ValidateImageEditParameters(request); err != nil {
		return openai.ImageGenerationResponse{}, err
	}
	if !p.upstreamStream {
		return openai.ImageGenerationResponse{}, ErrStreamingUnsupported
	}
	httpReq, err := p.imageEditRequest(ctx, request, true)
	if err != nil {
		return openai.ImageGenerationResponse{}, err
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
		return openai.ImageGenerationResponse{}, errors.New("provider returned a non-SSE image edit stream")
	}
	return streamImageEdit(&responseStreamReader{source: response.Body, remaining: maxImageGenerationStreamBytes}, request, write)
}

func (p OpenAICompatible) imageEditRequest(ctx context.Context, request openai.ImageEditRequest, stream bool) (*http.Request, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := map[string]string{
		"model": request.Model, "prompt": request.Prompt, "quality": request.Quality, "response_format": request.ResponseFormat,
		"size": request.Size, "user": request.User, "background": request.Background, "output_format": request.OutputFormat,
	}
	if request.N != nil {
		fields["n"] = strconv.Itoa(*request.N)
	}
	if request.OutputCompression != nil {
		fields["output_compression"] = strconv.Itoa(*request.OutputCompression)
	}
	if stream {
		fields["stream"] = "true"
		if request.PartialImages != nil {
			fields["partial_images"] = strconv.Itoa(*request.PartialImages)
		}
	}
	for name, value := range fields {
		if value != "" {
			if err := writer.WriteField(name, value); err != nil {
				return nil, err
			}
		}
	}
	imageField := "image"
	if len(request.Images) > 1 {
		imageField = "image[]"
	}
	for index, attachment := range request.Images {
		if err := writeImagePart(writer, imageField, fmt.Sprintf("image-%d%s", index+1, imageExtension(attachment.MediaType)), attachment); err != nil {
			return nil, err
		}
	}
	if request.Mask != nil {
		if err := writeImagePart(writer, "mask", "mask"+imageExtension(request.Mask.MediaType), *request.Mask); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "images/edits"), &body)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", writer.FormDataContentType())
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	return httpReq, nil
}

func writeImagePart(writer *multipart.Writer, field, filename string, attachment openai.ImageAttachment) error {
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, filepath.Base(filename)))
	header.Set("Content-Type", attachment.MediaType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	decoded, err := base64.StdEncoding.DecodeString(attachment.Data)
	if err != nil {
		return err
	}
	_, err = part.Write(decoded)
	return err
}

func imageExtension(mediaType string) string {
	switch mediaType {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}
