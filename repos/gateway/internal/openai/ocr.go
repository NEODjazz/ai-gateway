package openai

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	MaxOCRPages             = 1000
	MaxOCRPageIndex         = 100000
	MaxOCRDocumentBytes     = 16 << 20
	MaxOCRResponseBytes     = 32 << 20
	MaxOCRAnnotationPrompt  = 32 << 10
	MaxOCRAnnotationFormat  = 128 << 10
	MaxOCRExtractedImages   = 1000
	MaxOCRExtractedImageMin = 100000
)

type OCRDocument struct {
	Type        string `json:"type"`
	DocumentURL string `json:"document_url,omitempty"`
	ImageURL    string `json:"image_url,omitempty"`
}

type OCRRequest struct {
	Provider                    string          `json:"provider,omitempty"`
	Model                       string          `json:"model"`
	Document                    OCRDocument     `json:"document"`
	Pages                       any             `json:"pages,omitempty"`
	IncludeImageBase64          *bool           `json:"include_image_base64,omitempty"`
	ImageLimit                  *int            `json:"image_limit,omitempty"`
	ImageMinSize                *int            `json:"image_min_size,omitempty"`
	TableFormat                 string          `json:"table_format,omitempty"`
	ExtractHeader               *bool           `json:"extract_header,omitempty"`
	ExtractFooter               *bool           `json:"extract_footer,omitempty"`
	IncludeBlocks               *bool           `json:"include_blocks,omitempty"`
	ConfidenceScoresGranularity string          `json:"confidence_scores_granularity,omitempty"`
	DocumentAnnotationFormat    *ResponseFormat `json:"document_annotation_format,omitempty"`
	DocumentAnnotationPrompt    string          `json:"document_annotation_prompt,omitempty"`
	BBoxAnnotationFormat        *ResponseFormat `json:"bbox_annotation_format,omitempty"`
}

type OCRResponse struct {
	Pages              []json.RawMessage `json:"pages"`
	Model              string            `json:"model"`
	UsageInfo          OCRUsageInfo      `json:"usage_info"`
	DocumentAnnotation *string           `json:"document_annotation,omitempty"`
}

type OCRUsageInfo struct {
	PagesProcessed int  `json:"pages_processed"`
	DocSizeBytes   *int `json:"doc_size_bytes,omitempty"`
}

func (r OCRRequest) Validate() string {
	if strings.TrimSpace(r.Model) == "" {
		return "model is required"
	}
	if err := r.Document.validate(); err != nil {
		return err.Error()
	}
	if _, _, err := OCRPageSelection(r.Pages); err != nil {
		return err.Error()
	}
	if r.ImageLimit != nil && (*r.ImageLimit < 0 || *r.ImageLimit > MaxOCRExtractedImages) {
		return "image_limit must be between 0 and 1000"
	}
	if r.ImageMinSize != nil && (*r.ImageMinSize < 0 || *r.ImageMinSize > MaxOCRExtractedImageMin) {
		return "image_min_size must be between 0 and 100000"
	}
	if r.TableFormat != "" && r.TableFormat != "markdown" && r.TableFormat != "html" {
		return "table_format must be markdown or html"
	}
	if r.ConfidenceScoresGranularity != "" && r.ConfidenceScoresGranularity != "word" && r.ConfidenceScoresGranularity != "page" && r.ConfidenceScoresGranularity != "block" {
		return "confidence_scores_granularity must be word, page, or block"
	}
	if !utf8.ValidString(r.DocumentAnnotationPrompt) || len(r.DocumentAnnotationPrompt) > MaxOCRAnnotationPrompt {
		return "document_annotation_prompt exceeds the 32 KiB limit"
	}
	if r.DocumentAnnotationPrompt != "" && r.DocumentAnnotationFormat == nil {
		return "document_annotation_format is required with document_annotation_prompt"
	}
	for _, format := range []*ResponseFormat{r.DocumentAnnotationFormat, r.BBoxAnnotationFormat} {
		if err := validateOCRResponseFormat(format); err != nil {
			return err.Error()
		}
	}
	return ""
}

func (r OCRRequest) ReservePages() int {
	pages, specified, err := OCRPageSelection(r.Pages)
	if err != nil {
		return MaxOCRPages
	}
	if specified {
		return len(pages)
	}
	if r.Document.Type == "image_url" {
		return 1
	}
	return MaxOCRPages
}

func (r OCRRequest) InputTokens() int {
	return EstimateContextTokens(struct {
		Pages                       any
		TableFormat                 string
		ConfidenceScoresGranularity string
		DocumentAnnotationFormat    *ResponseFormat
		DocumentAnnotationPrompt    string
		BBoxAnnotationFormat        *ResponseFormat
	}{r.Pages, r.TableFormat, r.ConfidenceScoresGranularity, r.DocumentAnnotationFormat, r.DocumentAnnotationPrompt, r.BBoxAnnotationFormat})
}

func (d OCRDocument) Attachment() (*ImageAttachment, error) {
	value := d.value()
	if !strings.HasPrefix(value, "data:") {
		return nil, nil
	}
	header, data, found := strings.Cut(value, ",")
	if !found || !strings.HasSuffix(header, ";base64") || data == "" {
		return nil, errors.New("document must use a valid base64 data URL")
	}
	mediaType := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
	if base64.StdEncoding.DecodedLen(len(data)) > MaxOCRDocumentBytes {
		return nil, errors.New("document exceeds the 16 MiB limit")
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(decoded) == 0 {
		return nil, errors.New("document must use valid base64 data")
	}
	if mediaType == "application/pdf" {
		if d.Type != "document_url" || len(decoded) < 5 || string(decoded[:5]) != "%PDF-" {
			return nil, errors.New("document bytes do not match application/pdf")
		}
	} else {
		if d.Type != "image_url" {
			return nil, errors.New("document_url data must be an application/pdf")
		}
		attachment, parseErr := ParseDataImageURL(value)
		if parseErr != nil {
			return nil, parseErr
		}
		return &attachment, nil
	}
	return &ImageAttachment{MediaType: mediaType, Data: data}, nil
}

func (d OCRDocument) validate() error {
	if d.Type != "document_url" && d.Type != "image_url" {
		return errors.New("document.type must be document_url or image_url")
	}
	if d.Type == "document_url" && (d.DocumentURL == "" || d.ImageURL != "") || d.Type == "image_url" && (d.ImageURL == "" || d.DocumentURL != "") {
		return errors.New("document URL must match document.type")
	}
	value := d.value()
	if len(value) > MaxInferenceBodyBytes || !utf8.ValidString(value) {
		return errors.New("document URL exceeds its limit")
	}
	if strings.HasPrefix(value, "data:") {
		_, err := d.Attachment()
		return err
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || len(value) > 8192 {
		return errors.New("document URL must be an HTTPS URL without credentials or fragment")
	}
	return nil
}

func (d OCRDocument) value() string {
	if d.Type == "image_url" {
		return d.ImageURL
	}
	return d.DocumentURL
}

func OCRPageSelection(value any) ([]int, bool, error) {
	if value == nil {
		return nil, false, nil
	}
	var pages []int
	switch typed := value.(type) {
	case string:
		if typed == "" || len(typed) > 16<<10 {
			return nil, true, errors.New("pages must be a bounded page list")
		}
		seen := map[int]bool{}
		for _, part := range strings.Split(typed, ",") {
			part = strings.TrimSpace(part)
			bounds := strings.Split(part, "-")
			if len(bounds) > 2 || bounds[0] == "" {
				return nil, true, errors.New("pages contains an invalid range")
			}
			first, err := strconv.Atoi(bounds[0])
			if err != nil {
				return nil, true, errors.New("pages contains an invalid page")
			}
			last := first
			if len(bounds) == 2 {
				last, err = strconv.Atoi(bounds[1])
				if err != nil || last < first {
					return nil, true, errors.New("pages contains an invalid range")
				}
			}
			if first < 0 || last > MaxOCRPageIndex || last-first+1 > MaxOCRPages {
				return nil, true, errors.New("pages is outside the supported range")
			}
			for page := first; page <= last; page++ {
				if !seen[page] {
					seen[page] = true
					pages = append(pages, page)
					if len(pages) > MaxOCRPages {
						return nil, true, errors.New("pages may select at most 1000 pages")
					}
				}
			}
		}
	case []any:
		seen := map[int]bool{}
		for _, value := range typed {
			number, ok := value.(float64)
			page := int(number)
			if !ok || number != float64(page) || page < 0 || page > MaxOCRPageIndex || seen[page] {
				return nil, true, errors.New("pages must contain unique non-negative integers")
			}
			seen[page] = true
			pages = append(pages, page)
		}
	case []int:
		seen := map[int]bool{}
		for _, page := range typed {
			if page < 0 || page > MaxOCRPageIndex || seen[page] {
				return nil, true, errors.New("pages must contain unique non-negative integers")
			}
			seen[page] = true
			pages = append(pages, page)
		}
	default:
		return nil, true, errors.New("pages must be a page-range string or integer array")
	}
	if len(pages) == 0 || len(pages) > MaxOCRPages {
		return nil, true, errors.New("pages must select between 1 and 1000 pages")
	}
	return pages, true, nil
}

func validateOCRResponseFormat(format *ResponseFormat) error {
	if format == nil {
		return nil
	}
	if format.Type != "text" && format.Type != "json_object" && format.Type != "json_schema" {
		return errors.New("annotation format type must be text, json_object, or json_schema")
	}
	if format.Type == "json_schema" && (format.JSONSchema == nil || strings.TrimSpace(format.JSONSchema.Name) == "" || format.JSONSchema.Schema == nil) {
		return errors.New("json_schema annotation format requires a name and schema")
	}
	if format.Type != "json_schema" && format.JSONSchema != nil {
		return errors.New("json_schema is only valid with json_schema annotation format")
	}
	payload, err := json.Marshal(format)
	if err != nil || len(payload) > MaxOCRAnnotationFormat {
		return errors.New("annotation format exceeds the 128 KiB limit")
	}
	return nil
}
