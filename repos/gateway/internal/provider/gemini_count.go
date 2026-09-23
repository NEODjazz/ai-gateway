package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"ai-gateway-gateway/internal/openai"
)

func (g Gemini) CountTokens(ctx context.Context, request TokenCountRequest) (TokenCountResult, error) {
	chat := openai.ChatCompletionRequest{Model: request.Model, Messages: request.Messages, Tools: request.Tools, ToolChoice: request.ToolChoice, ParallelToolCalls: request.ParallelToolCalls, ChatGenerationOptions: request.ChatGenerationOptions, ResponseFormat: request.ResponseFormat, GeminiCachedContent: request.GeminiCachedContent, GeminiMediaResolution: request.GeminiMediaResolution, GeminiFileSearch: request.GeminiFileSearch, GeminiComputerUse: request.GeminiComputerUse}
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
	// The full native request includes system instructions and tool declarations;
	// sending only contents would undercount those parts of the context.
	var body []byte
	if g.vertex {
		body, err = json.Marshal(native)
	} else {
		body, err = json.Marshal(struct {
			Request any `json:"generateContentRequest"`
		}{struct {
			Model string `json:"model"`
			geminiRequest
		}{"models/" + model, native}})
	}
	if err != nil {
		return TokenCountResult{}, err
	}
	if len(body) > openai.MaxInferenceBodyBytes {
		return TokenCountResult{}, errors.New("token count request exceeds inference limit")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	endpoint, err := g.modelEndpoint(request.Model, "countTokens")
	if err != nil {
		return TokenCountResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return TokenCountResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := g.authorize(req); err != nil {
		return TokenCountResult{}, err
	}
	response, err := g.client.Do(req)
	if err != nil {
		return TokenCountResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return TokenCountResult{}, responseStatusError(g.providerName(), response)
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
	return TokenCountResult{InputTokens: *result.TotalTokens, Model: request.Model, Source: g.providerName()}, nil
}
