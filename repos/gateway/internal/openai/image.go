package openai

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
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
	Resolution        string `json:"resolution,omitempty"`
	AspectRatio       string `json:"aspect_ratio,omitempty"`
	Seed              *int64 `json:"seed,omitempty"`
}

type ImageEditRequest struct {
	Provider          string            `json:"provider,omitempty"`
	Model             string            `json:"model"`
	Prompt            string            `json:"prompt"`
	Images            []ImageAttachment `json:"images"`
	Mask              *ImageAttachment  `json:"mask,omitempty"`
	N                 *int              `json:"n,omitempty"`
	Quality           string            `json:"quality,omitempty"`
	ResponseFormat    string            `json:"response_format,omitempty"`
	Size              string            `json:"size,omitempty"`
	User              string            `json:"user,omitempty"`
	Background        string            `json:"background,omitempty"`
	OutputFormat      string            `json:"output_format,omitempty"`
	OutputCompression *int              `json:"output_compression,omitempty"`
}

type ImageVariationRequest struct {
	Provider       string          `json:"provider,omitempty"`
	Model          string          `json:"model"`
	Image          ImageAttachment `json:"image"`
	N              *int            `json:"n,omitempty"`
	ResponseFormat string          `json:"response_format,omitempty"`
	Size           string          `json:"size,omitempty"`
	User           string          `json:"user,omitempty"`
}

func (r ImageVariationRequest) Validate() string {
	if strings.TrimSpace(r.Model) == "" {
		return "model is required"
	}
	if err := ValidateImageAttachments([]ImageAttachment{r.Image}); err != nil {
		return err.Error()
	}
	if utf8.RuneCountInString(r.User) > 256 {
		return "user must contain at most 256 characters"
	}
	if r.N != nil && (*r.N < 1 || *r.N > MaxGeneratedImages) {
		return "n must be between 1 and 10"
	}
	if !oneOfOrEmpty(r.ResponseFormat, "url", "b64_json") {
		return "response_format must be url or b64_json"
	}
	if !oneOfOrEmpty(r.Size, "256x256", "512x512", "1024x1024") {
		return "unsupported size value"
	}
	return ""
}

func (r ImageVariationRequest) GenerationRequest() ImageGenerationRequest {
	return ImageGenerationRequest{Provider: r.Provider, Model: r.Model, Prompt: "variation", N: r.N, ResponseFormat: r.ResponseFormat, Size: r.Size, User: r.User}
}

func ImageVariationInputTokens(r ImageVariationRequest) int {
	return imageAttachmentTokens([]ImageAttachment{r.Image})
}

func ImageVariationReserveTokens(r ImageVariationRequest) int {
	return ReserveTokens(ImageVariationInputTokens(r), ImageGenerationOutputReserve(r.GenerationRequest()))
}

func (r ImageEditRequest) Validate() string {
	if len(r.Images) == 0 || len(r.Images) > MaxImageAttachments {
		return "between 1 and 8 images are required"
	}
	attachments := append([]ImageAttachment(nil), r.Images...)
	if r.Mask != nil {
		if r.Mask.MediaType != "image/png" {
			return "mask must be a PNG image"
		}
		attachments = append(attachments, *r.Mask)
	}
	if err := ValidateImageAttachments(attachments); err != nil {
		return err.Error()
	}
	return r.GenerationRequest().Validate()
}

func (r ImageEditRequest) GenerationRequest() ImageGenerationRequest {
	return ImageGenerationRequest{
		Provider: r.Provider, Model: r.Model, Prompt: r.Prompt, N: r.N, Quality: r.Quality,
		ResponseFormat: r.ResponseFormat, Size: r.Size, User: r.User, Background: r.Background,
		OutputFormat: r.OutputFormat, OutputCompression: r.OutputCompression,
	}
}

func ImageEditReserveTokens(r ImageEditRequest) int {
	return ReserveTokens(ImageEditInputTokens(r), ImageGenerationOutputReserve(r.GenerationRequest()))
}

func ImageEditInputTokens(r ImageEditRequest) int {
	tokens := EstimateContextTokens(r.Prompt)
	attachments := append([]ImageAttachment(nil), r.Images...)
	if r.Mask != nil {
		attachments = append(attachments, *r.Mask)
	}
	attachmentTokens := imageAttachmentTokens(attachments)
	limit := int(^uint(0) >> 1)
	if tokens > limit-attachmentTokens {
		return limit
	}
	return tokens + attachmentTokens
}

func imageAttachmentTokens(attachments []ImageAttachment) int {
	tokens := 0
	limit := int(^uint(0) >> 1)
	for _, attachment := range attachments {
		decoded, err := base64.StdEncoding.DecodeString(attachment.Data)
		if err != nil {
			return limit
		}
		imageTokens := len(decoded) / 4
		if len(decoded)%4 != 0 {
			imageTokens++
		}
		if tokens > limit-imageTokens {
			return limit
		}
		tokens += imageTokens
	}
	return tokens
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
	if !oneOfOrEmpty(r.OutputFormat, "png", "webp", "jpeg", "svg") {
		return "unsupported output_format value"
	}
	if !oneOfOrEmpty(r.Resolution, "512", "1K", "2K", "4K") {
		return "resolution must be 512, 1K, 2K, or 4K"
	}
	if !validImageAspectRatio(r.AspectRatio) {
		return "aspect_ratio must be auto or a ratio between 1:1 and 99:99"
	}
	return ""
}

func validImageAspectRatio(value string) bool {
	if value == "" || value == "auto" {
		return true
	}
	width, height, found := strings.Cut(value, ":")
	if !found || strings.Contains(height, ":") {
		return false
	}
	w, wErr := strconv.ParseFloat(width, 64)
	h, hErr := strconv.ParseFloat(height, 64)
	return wErr == nil && hErr == nil && w >= 1 && w <= 99 && h >= 1 && h <= 99
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
	MediaType     string `json:"media_type,omitempty"`
}

type ImageUsage struct {
	ProviderCostUSDTicks *int64 `json:"-"`
	InputTokens          int    `json:"input_tokens"`
	OutputTokens         int    `json:"output_tokens"`
	TotalTokens          int    `json:"total_tokens"`
}

func (u *ImageUsage) UnmarshalJSON(data []byte) error {
	type imageUsage ImageUsage
	var wire struct {
		imageUsage
		ProviderCostUSDTicks *int64 `json:"cost_in_usd_ticks"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*u = ImageUsage(wire.imageUsage)
	u.ProviderCostUSDTicks = wire.ProviderCostUSDTicks
	return nil
}
