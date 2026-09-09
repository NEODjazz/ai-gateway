package openai

import (
	"strings"
	"unicode/utf8"
)

const MaxGeneratedImages = 10

type ImageGenerationRequest struct {
	Provider          string `json:"provider,omitempty"`
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
}

func (r ImageGenerationRequest) Validate() string {
	if strings.TrimSpace(r.Model) == "" || strings.TrimSpace(r.Prompt) == "" {
		return "model and prompt are required"
	}
	if utf8.RuneCountInString(r.Prompt) > 32000 {
		return "prompt must contain at most 32000 characters"
	}
	if utf8.RuneCountInString(r.User) > 256 {
		return "user must contain at most 256 characters"
	}
	if r.N != nil && (*r.N < 1 || *r.N > MaxGeneratedImages) {
		return "n must be between 1 and 10"
	}
	if r.OutputCompression != nil && (*r.OutputCompression < 0 || *r.OutputCompression > 100) {
		return "output_compression must be between 0 and 100"
	}
	if !oneOfOrEmpty(r.Quality, "auto", "low", "medium", "high", "xhigh", "max") {
		return "unsupported quality value"
	}
	if !oneOfOrEmpty(r.ResponseFormat, "url", "b64_json") {
		return "response_format must be url or b64_json"
	}
	if !oneOfOrEmpty(r.Size, "auto", "256x256", "512x512", "1024x1024", "1536x1024", "1024x1536", "1792x1024", "1024x1792") {
		return "unsupported size value"
	}
	if !oneOfOrEmpty(r.Style, "vivid", "natural") {
		return "style must be vivid or natural"
	}
	if !oneOfOrEmpty(r.Background, "auto", "transparent", "opaque") {
		return "unsupported background value"
	}
	if !oneOfOrEmpty(r.OutputFormat, "png", "webp", "jpeg") {
		return "unsupported output_format value"
	}
	return ""
}

func ImageGenerationOutputReserve(r ImageGenerationRequest) int {
	count := 1
	if r.N != nil {
		count = *r.N
	}
	if count <= 0 {
		return 0
	}
	limit := int(^uint(0) >> 1)
	if count > limit/DefaultOutputTokenReserve {
		return limit
	}
	return count * DefaultOutputTokenReserve
}

func ImageGenerationReserveTokens(r ImageGenerationRequest) int {
	return ReserveTokens(EstimateContextTokens(r.Prompt), ImageGenerationOutputReserve(r))
}

func oneOfOrEmpty(value string, allowed ...string) bool {
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

type ImageGenerationResponse struct {
	Created      int64       `json:"created"`
	Data         []ImageData `json:"data,omitempty"`
	Background   string      `json:"background,omitempty"`
	OutputFormat string      `json:"output_format,omitempty"`
	Quality      string      `json:"quality,omitempty"`
	Size         string      `json:"size,omitempty"`
	Usage        *ImageUsage `json:"usage,omitempty"`
}

type ImageData struct {
	B64JSON       string `json:"b64_json,omitempty"`
	URL           string `json:"url,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

type ImageUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}
