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

func (Mistral) SupportsOCR() bool { return true }

func (Mistral) ValidateOCRParameters(request openai.OCRRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: "mistral", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	if request.Document.Type == "file" {
		return &Error{Class: FailureClientRequest, Provider: "mistral", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New("OCR file reference was not resolved")}
	}
	return nil
}

func (p Mistral) OCR(ctx context.Context, request openai.OCRRequest) (openai.OCRResponse, error) {
	if err := p.ValidateOCRParameters(request); err != nil {
		return openai.OCRResponse{}, err
	}
	payload, err := json.Marshal(struct {
		Model                       string                 `json:"model"`
		Document                    openai.OCRDocument     `json:"document"`
		Pages                       any                    `json:"pages,omitempty"`
		IncludeImageBase64          *bool                  `json:"include_image_base64,omitempty"`
		ImageLimit                  *int                   `json:"image_limit,omitempty"`
		ImageMinSize                *int                   `json:"image_min_size,omitempty"`
		TableFormat                 string                 `json:"table_format,omitempty"`
		ExtractHeader               *bool                  `json:"extract_header,omitempty"`
		ExtractFooter               *bool                  `json:"extract_footer,omitempty"`
		IncludeBlocks               *bool                  `json:"include_blocks,omitempty"`
		ConfidenceScoresGranularity string                 `json:"confidence_scores_granularity,omitempty"`
		DocumentAnnotationFormat    *openai.ResponseFormat `json:"document_annotation_format,omitempty"`
		DocumentAnnotationPrompt    string                 `json:"document_annotation_prompt,omitempty"`
		BBoxAnnotationFormat        *openai.ResponseFormat `json:"bbox_annotation_format,omitempty"`
	}{request.Model, request.Document, request.Pages, request.IncludeImageBase64, request.ImageLimit, request.ImageMinSize, request.TableFormat, request.ExtractHeader, request.ExtractFooter, request.IncludeBlocks, request.ConfidenceScoresGranularity, request.DocumentAnnotationFormat, request.DocumentAnnotationPrompt, request.BBoxAnnotationFormat})
	if err != nil {
		return openai.OCRResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "ocr"), bytes.NewReader(payload))
	if err != nil {
		return openai.OCRResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return openai.OCRResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.OCRResponse{}, responseStatusError("mistral", response)
	}
	return decodeOCRResponse(response.Body)
}

func decodeOCRResponse(reader io.Reader) (openai.OCRResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, openai.MaxOCRResponseBytes+1))
	if err != nil {
		return openai.OCRResponse{}, err
	}
	if len(payload) > openai.MaxOCRResponseBytes {
		return openai.OCRResponse{}, errors.New("OCR response exceeds the 32 MiB limit")
	}
	var response openai.OCRResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return openai.OCRResponse{}, err
	}
	if err := validateOCRResponse(response); err != nil {
		return openai.OCRResponse{}, err
	}
	return response, nil
}

func validateOCRResponse(response openai.OCRResponse) error {
	if strings.TrimSpace(response.Model) == "" || len(response.Model) > 256 || len(response.Pages) == 0 || len(response.Pages) > openai.MaxOCRPages || response.UsageInfo.PagesProcessed != len(response.Pages) {
		return errors.New("provider returned invalid OCR usage")
	}
	if response.UsageInfo.DocSizeBytes != nil && (*response.UsageInfo.DocSizeBytes < 0 || *response.UsageInfo.DocSizeBytes > 1<<30) {
		return errors.New("provider returned invalid OCR document size")
	}
	if response.DocumentAnnotation != nil && len(*response.DocumentAnnotation) > 4<<20 {
		return errors.New("provider returned oversized OCR document annotation")
	}
	seen := make(map[int]bool, len(response.Pages))
	for _, raw := range response.Pages {
		var page struct {
			Index    *int             `json:"index"`
			Markdown *string          `json:"markdown"`
			Images   *json.RawMessage `json:"images,omitempty"`
		}
		if len(raw) == 0 || json.Unmarshal(raw, &page) != nil || page.Index == nil || *page.Index < 0 || *page.Index > openai.MaxOCRPageIndex || seen[*page.Index] || page.Markdown == nil || len(*page.Markdown) > 4<<20 {
			return errors.New("provider returned an invalid OCR page")
		}
		seen[*page.Index] = true
		if page.Images != nil {
			var images []json.RawMessage
			if json.Unmarshal(*page.Images, &images) != nil || len(images) > openai.MaxOCRExtractedImages {
				return errors.New("provider returned invalid OCR page images")
			}
		}
	}
	return nil
}
