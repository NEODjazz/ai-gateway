package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

func (Gemini) SupportsOCR() bool { return true }

func (g Gemini) OCR(ctx context.Context, request openai.OCRRequest) (openai.OCRResponse, error) {
	attachment, pages, err := validateGeminiOCRRequest(request)
	if err != nil {
		return openai.OCRResponse{}, err
	}
	decoded, err := base64.StdEncoding.DecodeString(attachment.Data)
	if err != nil {
		return openai.OCRResponse{}, geminiInvalid("document")
	}
	prompt := "Transcribe each requested page exactly. Preserve headings, paragraphs, lists and tables as Markdown. Return only JSON matching the response schema."
	if pages != nil {
		encodedPages, _ := json.Marshal(pages)
		prompt += " Return exactly these zero-based page indices: " + string(encodedPages) + "."
	} else if request.Document.Type == "image_url" {
		prompt += " Treat the image as page index 0."
	}
	body := geminiRequest{
		Contents: []geminiContent{{Parts: []geminiPart{
			{InlineData: &geminiInlineData{MIMEType: attachment.MediaType, Data: attachment.Data}},
			{Text: prompt},
		}}},
		Generation: geminiGeneration{
			ResponseMIMEType: "application/json",
			ResponseJSONSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pages": map[string]any{
						"type": "array", "minItems": 1, "maxItems": openai.MaxOCRPages,
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"index":    map[string]any{"type": "integer", "minimum": 0, "maximum": openai.MaxOCRPageIndex},
								"markdown": map[string]any{"type": "string"},
							},
							"required":             []string{"index", "markdown"},
							"additionalProperties": false,
						},
					},
				},
				"required":             []string{"pages"},
				"additionalProperties": false,
			},
		},
	}
	response, err := g.generateOCR(ctx, request.Model, body)
	if err != nil {
		return openai.OCRResponse{}, err
	}
	result, err := decodeGeminiOCRResponse(response, request.Model, len(decoded))
	if err != nil {
		return openai.OCRResponse{}, err
	}
	if pages != nil {
		actual := make([]int, 0, len(result.Pages))
		for _, raw := range result.Pages {
			var page struct {
				Index int `json:"index"`
			}
			_ = json.Unmarshal(raw, &page)
			actual = append(actual, page.Index)
		}
		if !slices.Equal(actual, pages) {
			return openai.OCRResponse{}, errors.New("Gemini returned unexpected OCR pages")
		}
	}
	return result, nil
}

func validateGeminiOCRRequest(request openai.OCRRequest) (openai.ImageAttachment, []int, error) {
	if message := request.Validate(); message != "" {
		return openai.ImageAttachment{}, nil, &Error{Class: FailureClientRequest, Provider: "gemini", StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	if err := rejectParameters("gemini",
		parameterCheck{"include_image_base64", request.IncludeImageBase64 != nil},
		parameterCheck{"image_limit", request.ImageLimit != nil},
		parameterCheck{"image_min_size", request.ImageMinSize != nil},
		parameterCheck{"table_format", request.TableFormat != "" && request.TableFormat != "markdown"},
		parameterCheck{"extract_header", request.ExtractHeader != nil},
		parameterCheck{"extract_footer", request.ExtractFooter != nil},
		parameterCheck{"include_blocks", request.IncludeBlocks != nil},
		parameterCheck{"confidence_scores_granularity", request.ConfidenceScoresGranularity != ""},
		parameterCheck{"document_annotation_format", request.DocumentAnnotationFormat != nil},
		parameterCheck{"document_annotation_prompt", request.DocumentAnnotationPrompt != ""},
		parameterCheck{"bbox_annotation_format", request.BBoxAnnotationFormat != nil},
	); err != nil {
		return openai.ImageAttachment{}, nil, err
	}
	attachment, err := request.Document.Attachment()
	if err != nil {
		return openai.ImageAttachment{}, nil, geminiInvalid("document")
	}
	if attachment == nil || !oneOfOrEmptyImageValue(attachment.MediaType, "application/pdf", "image/png", "image/jpeg", "image/webp") {
		return openai.ImageAttachment{}, nil, &Error{Class: FailureClientRequest, Provider: "gemini", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: "document", Err: errors.New("Gemini OCR requires an inline PDF, PNG, JPEG, or WebP document")}
	}
	pages, specified, err := openai.OCRPageSelection(request.Pages)
	if err != nil {
		return openai.ImageAttachment{}, nil, geminiInvalid("pages")
	}
	if request.Document.Type == "image_url" {
		if specified && !slices.Equal(pages, []int{0}) {
			return openai.ImageAttachment{}, nil, geminiInvalid("pages")
		}
		return *attachment, []int{0}, nil
	}
	if specified {
		return *attachment, pages, nil
	}
	return *attachment, nil, nil
}

func (g Gemini) generateOCR(ctx context.Context, modelName string, body geminiRequest) (io.ReadCloser, error) {
	model := strings.TrimPrefix(modelName, "models/")
	if model == "" || strings.ContainsAny(model, "/\\?#%") || model == "." || model == ".." {
		return nil, geminiInvalid("model")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	base, err := url.Parse(g.baseURL)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("invalid Gemini base URL")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiBaseURL(g.baseURL)+"/models/"+url.PathEscape(model)+":generateContent", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if err := g.authorize(httpRequest); err != nil {
		return nil, err
	}
	response, err := g.client.Do(httpRequest)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		return nil, responseStatusError("gemini", response)
	}
	return response.Body, nil
}

func decodeGeminiOCRResponse(reader io.ReadCloser, requestModel string, documentBytes int) (openai.OCRResponse, error) {
	defer reader.Close()
	payload, err := io.ReadAll(io.LimitReader(reader, openai.MaxOCRResponseBytes+1))
	if err != nil || len(payload) > openai.MaxOCRResponseBytes {
		return openai.OCRResponse{}, errors.New("Gemini OCR response exceeds the 32 MiB limit")
	}
	var upstream geminiResponse
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if decoder.Decode(&upstream) != nil || decoder.Decode(&struct{}{}) != io.EOF || len(upstream.Candidates) != 1 || upstream.Usage == nil {
		return openai.OCRResponse{}, errors.New("Gemini returned invalid OCR response")
	}
	usage := upstream.Usage
	if usage.Prompt < 0 || usage.Candidates < 0 || usage.Thoughts < 0 || usage.Total <= 0 || usage.Total < usage.Prompt || usage.Candidates > int(^uint(0)>>1)-usage.Thoughts || usage.Total < usage.Prompt+usage.Candidates+usage.Thoughts {
		return openai.OCRResponse{}, errors.New("Gemini returned inconsistent OCR usage")
	}
	parts := upstream.Candidates[0].Content.Parts
	if len(parts) != 1 || parts[0].Text == "" || parts[0].Thought || parts[0].InlineData != nil || parts[0].FunctionCall != nil || parts[0].FunctionResponse != nil {
		return openai.OCRResponse{}, errors.New("Gemini returned invalid OCR content")
	}
	var structured struct {
		Pages []json.RawMessage `json:"pages"`
	}
	structuredDecoder := json.NewDecoder(strings.NewReader(parts[0].Text))
	structuredDecoder.DisallowUnknownFields()
	if structuredDecoder.Decode(&structured) != nil || structuredDecoder.Decode(&struct{}{}) != io.EOF {
		return openai.OCRResponse{}, errors.New("Gemini returned invalid structured OCR content")
	}
	for _, raw := range structured.Pages {
		var page struct {
			Index    int    `json:"index"`
			Markdown string `json:"markdown"`
		}
		pageDecoder := json.NewDecoder(bytes.NewReader(raw))
		pageDecoder.DisallowUnknownFields()
		if pageDecoder.Decode(&page) != nil || pageDecoder.Decode(&struct{}{}) != io.EOF {
			return openai.OCRResponse{}, errors.New("Gemini returned invalid structured OCR page")
		}
	}
	model := upstream.Model
	if model == "" {
		model = requestModel
	}
	result := openai.OCRResponse{Pages: structured.Pages, Model: model, UsageInfo: openai.OCRUsageInfo{PagesProcessed: len(structured.Pages), DocSizeBytes: &documentBytes}}
	if err := validateOCRResponse(result); err != nil {
		return openai.OCRResponse{}, fmt.Errorf("invalid Gemini OCR result: %w", err)
	}
	return result, nil
}
