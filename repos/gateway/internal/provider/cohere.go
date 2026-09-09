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

const maxCohereRerankResponseBytes = 8 << 20

type Cohere struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

type cohereRerankRequest struct {
	Model           string   `json:"model"`
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	TopN            *int     `json:"top_n,omitempty"`
	MaxTokensPerDoc *int     `json:"max_tokens_per_doc,omitempty"`
}

func NewCohere(baseURL, apiKey string) Cohere {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.cohere.com"
	}
	return Cohere{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, client: newProviderHTTPClient(180 * time.Second)}
}

func (Cohere) SupportsResponses() bool { return false }

func (Cohere) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, rejectParameters("cohere", parameterCheck{"chat_completions", true})
}

func (Cohere) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, rejectParameters("cohere", parameterCheck{"responses", true})
}

func (p Cohere) Rerank(ctx context.Context, request openai.RerankRequest) (openai.RerankResponse, error) {
	if err := rejectParameters("cohere",
		parameterCheck{"rank_fields", len(request.RankFields) > 0},
		parameterCheck{"max_chunks_per_doc", request.MaxChunksPerDoc != nil},
	); err != nil {
		return openai.RerankResponse{}, err
	}
	documents := make([]string, len(request.Documents))
	for index, document := range request.Documents {
		text, ok := document.(string)
		if !ok {
			return openai.RerankResponse{}, &Error{Class: FailureClientRequest, Provider: "cohere", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: "documents", Err: errors.New("Cohere v2 rerank requires string documents")}
		}
		documents[index] = text
	}
	body, err := json.Marshal(cohereRerankRequest{Model: request.Model, Query: request.Query, Documents: documents, TopN: request.TopN, MaxTokensPerDoc: request.MaxTokensPerDoc})
	if err != nil {
		return openai.RerankResponse{}, err
	}
	endpoint, err := cohereEndpoint(p.baseURL, "v2/rerank")
	if err != nil {
		return openai.RerankResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return openai.RerankResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	if p.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return openai.RerankResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return openai.RerankResponse{}, responseStatusError("cohere", response)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxCohereRerankResponseBytes+1))
	if err != nil {
		return openai.RerankResponse{}, err
	}
	if len(payload) > maxCohereRerankResponseBytes {
		return openai.RerankResponse{}, errors.New("Cohere rerank response exceeds limit")
	}
	var result openai.RerankResponse
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&result); err != nil {
		return openai.RerankResponse{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return openai.RerankResponse{}, errors.New("Cohere rerank response contains trailing data")
	}
	return result, nil
}

func cohereEndpoint(baseURL, suffix string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", errors.New("invalid Cohere base URL")
	}
	path := strings.TrimRight(base.Path, "/")
	if strings.HasSuffix(path, "/v1") || strings.HasSuffix(path, "/v2") {
		path = path[:len(path)-3]
	}
	base.Path = strings.TrimRight(path, "/") + "/" + suffix
	return base.String(), nil
}
