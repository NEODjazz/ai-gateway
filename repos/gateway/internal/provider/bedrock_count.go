package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"ai-gateway-gateway/internal/openai"
)

const maxBedrockTokenCountResponseBytes = 64 << 10

func (b Bedrock) CountTokens(ctx context.Context, request TokenCountRequest) (TokenCountResult, error) {
	chat := openai.ChatCompletionRequest{Model: request.Model, Messages: request.Messages, Tools: request.Tools, ToolChoice: request.ToolChoice, ParallelToolCalls: request.ParallelToolCalls, ChatGenerationOptions: request.ChatGenerationOptions, ResponseFormat: request.ResponseFormat}
	if err := validateTokenCountRequest(chat); err != nil {
		return TokenCountResult{}, err
	}
	converse, err := bedrockChatRequest(chat)
	if err != nil {
		return TokenCountResult{}, err
	}
	body := struct {
		Input struct {
			Converse bedrockRequest `json:"converse"`
		} `json:"input"`
	}{}
	body.Input.Converse = converse
	payload, err := json.Marshal(body)
	if err != nil {
		return TokenCountResult{}, err
	}
	if len(payload) > openai.MaxInferenceBodyBytes {
		return TokenCountResult{}, errors.New("token count request exceeds inference limit")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	endpoint := b.baseURL + "/model/" + url.PathEscape(request.Model) + "/count-tokens"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return TokenCountResult{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if err := b.authorize(ctx, httpRequest, payload); err != nil {
		return TokenCountResult{}, err
	}
	response, err := b.client.Do(httpRequest)
	if err != nil {
		return TokenCountResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return TokenCountResult{}, responseStatusError("bedrock", response)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBedrockTokenCountResponseBytes+1))
	if err != nil {
		return TokenCountResult{}, err
	}
	if len(data) > maxBedrockTokenCountResponseBytes {
		return TokenCountResult{}, errors.New("token count response exceeds limit")
	}
	var result struct {
		InputTokens *int `json:"inputTokens"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return TokenCountResult{}, err
	}
	if result.InputTokens == nil || *result.InputTokens < 0 {
		return TokenCountResult{}, errors.New("invalid provider token count")
	}
	return TokenCountResult{InputTokens: *result.InputTokens, Model: request.Model, Source: "bedrock"}, nil
}
