package provider

import (
	"context"
	"net/url"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

// VertexGemini exposes the stable Vertex publisher-model GenerateContent
// surface while keeping its workload identity and capability set explicit.
type VertexGemini struct {
	gemini Gemini
}

func NewVertexGemini(baseURL string, stream bool) VertexGemini {
	gemini := NewGeminiWithAuth(baseURL, "", stream, "gcp_adc")
	gemini.vertex = true
	gemini.errorProvider = "vertex-gemini"
	return VertexGemini{gemini: gemini}
}

func (v VertexGemini) ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return v.gemini.ChatCompletions(ctx, request)
}

func (v VertexGemini) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	return v.gemini.StreamChatCompletions(ctx, request, write)
}

func (v VertexGemini) Responses(ctx context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	return v.gemini.Responses(ctx, request)
}

func (v VertexGemini) CountTokens(ctx context.Context, request TokenCountRequest) (TokenCountResult, error) {
	return v.gemini.CountTokens(ctx, request)
}

func (v VertexGemini) ValidateChatParameters(request openai.ChatCompletionRequest) error {
	return v.gemini.ValidateChatParameters(request)
}

func (v VertexGemini) ValidateResponseParameters(request openai.ResponseRequest) error {
	return v.gemini.ValidateResponseParameters(request)
}

func (VertexGemini) SupportsResponses() bool        { return false }
func (VertexGemini) SupportsTools() bool            { return true }
func (VertexGemini) SupportsStructuredOutput() bool { return true }
func (VertexGemini) SupportsVision() bool           { return true }
func (VertexGemini) SupportsWebSearch() bool        { return true }
func (VertexGemini) SupportsCodeExecution() bool    { return true }
func (VertexGemini) SupportsURLContext() bool       { return true }
func (VertexGemini) SupportsAudioInput() bool       { return true }
func (VertexGemini) SupportsAudioTimestamp() bool   { return true }
func (VertexGemini) SupportsVideoInput() bool       { return true }
func (VertexGemini) SupportsFileInput() bool        { return true }
func (VertexGemini) SupportsReasoningBlocks() bool  { return true }
func (VertexGemini) SupportsUnsignedReasoning() bool {
	return true
}

func validVertexGeminiBaseURL(value string) bool {
	parsed, err := url.ParseRequestURI(strings.TrimRight(strings.TrimSpace(value), "/"))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 7 || parts[0] != "v1" || parts[1] != "projects" || !validVertexResourceSegment(parts[2]) || parts[3] != "locations" || !validVertexResourceSegment(parts[4]) || parts[5] != "publishers" || parts[6] != "google" {
		return false
	}
	return true
}

func validManagedVertexGeminiBaseURL(value string) bool {
	if !validVertexGeminiBaseURL(value) {
		return false
	}
	parsed, _ := url.Parse(strings.TrimRight(strings.TrimSpace(value), "/"))
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	return parsed.Scheme == "https" && strings.EqualFold(parsed.Host, parts[4]+"-aiplatform.googleapis.com")
}

func validVertexResourceSegment(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' && character != '_' && character != '.' && character != ':' {
			return false
		}
	}
	return true
}
