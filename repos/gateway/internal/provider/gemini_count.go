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

func (g Gemini) CountTokens(ctx context.Context, request TokenCountRequest) (TokenCountResult, error) {
	chat := openai.ChatCompletionRequest{Model: request.Model, Messages: request.Messages, Tools: request.Tools, ToolChoice: request.ToolChoice, ParallelToolCalls: request.ParallelToolCalls}
	if err := validateTokenCountRequest(chat); err != nil {
		return TokenCountResult{}, err
	}
	native, err := geminiChatRequest(chat)
	if err != nil {
		return TokenCountResult{}, err
	}
	model := strings.TrimPrefix(request.Model, "models/")
	if model == "" || strings.ContainsAny(model, "/\\?#%") || model == "." || model == ".." {
		return TokenCountResult{}, geminiInvalid("model")
	}
	base, err := url.Parse(g.baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return TokenCountResult{}, errors.New("invalid Gemini base URL")
	}
	// The full native request includes system instructions and tool declarations;
	// sending only contents would undercount those parts of the context.
	body, err := json.Marshal(struct {
		Request any `json:"generateContentRequest"`
	}{struct {
		Model string `json:"model"`
		geminiRequest
	}{"models/" + model, native}})
	if err != nil {
		return TokenCountResult{}, err
	}
	if len(body) > openai.MaxInferenceBodyBytes {
		return TokenCountResult{}, errors.New("token count request exceeds inference limit")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	endpoint := geminiBaseURL(g.baseURL) + "/models/" + url.PathEscape(model) + ":countTokens"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return TokenCountResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.apiKey)
	response, err := g.client.Do(req)
	if err != nil {
		return TokenCountResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return TokenCountResult{}, responseStatusError("gemini", response)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil {
		return TokenCountResult{}, err
	}
	if len(payload) > 64<<10 {
		return TokenCountResult{}, errors.New("token count response exceeds limit")
	}
	var result struct {
		TotalTokens *int `json:"totalTokens"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return TokenCountResult{}, err
	}
	if result.TotalTokens == nil || *result.TotalTokens < 0 {
		return TokenCountResult{}, errors.New("invalid provider token count")
	}
	return TokenCountResult{InputTokens: *result.TotalTokens, Model: request.Model, Source: "gemini"}, nil
}
