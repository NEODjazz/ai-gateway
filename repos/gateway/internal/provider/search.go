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

	"ai-gateway-gateway/internal/openai"
)

const maxSearchResponseBytes = 8 << 20

func (OpenAICompatible) SupportsSearch() bool { return true }

func (p OpenAICompatible) ValidateSearchParameters(request openai.SearchRequest) error {
	if message := request.Validate(); message != "" {
		return &Error{Class: FailureClientRequest, Provider: p.providerName(), StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	return nil
}

func (p OpenAICompatible) Search(ctx context.Context, request openai.SearchRequest) (openai.SearchResponse, error) {
	if err := p.ValidateSearchParameters(request); err != nil {
		return openai.SearchResponse{}, err
	}
	payload, err := json.Marshal(struct {
		Query              any      `json:"query"`
		MaxResults         *int     `json:"max_results,omitempty"`
		SearchDomainFilter []string `json:"search_domain_filter,omitempty"`
		MaxTokensPerPage   *int     `json:"max_tokens_per_page,omitempty"`
		Country            string   `json:"country,omitempty"`
	}{request.Query, request.MaxResults, request.SearchDomainFilter, request.MaxTokensPerPage, request.Country})
	if err != nil {
		return openai.SearchResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(p.baseURL, "search"), bytes.NewReader(payload))
	if err != nil {
		return openai.SearchResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return openai.SearchResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.SearchResponse{}, responseStatusError(p.providerName(), response)
	}
	result, err := decodeSearchResponse(response.Body)
	if err != nil {
		return openai.SearchResponse{}, err
	}
	resultLimit := 10
	if request.MaxResults != nil {
		resultLimit = *request.MaxResults
	}
	if len(result.Results) > resultLimit {
		return openai.SearchResponse{}, errors.New("provider returned more search results than requested")
	}
	result.Model, _ = request.RoutingModel()
	result.Usage.SearchRequests = request.SearchUnits()
	return result, nil
}

func decodeSearchResponse(reader io.Reader) (openai.SearchResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxSearchResponseBytes+1))
	if err != nil {
		return openai.SearchResponse{}, err
	}
	if len(payload) > maxSearchResponseBytes {
		return openai.SearchResponse{}, errors.New("search response exceeds limit")
	}
	var response openai.SearchResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return openai.SearchResponse{}, err
	}
	if err := validateSearchResponse(response, openai.MaxSearchResults); err != nil {
		return openai.SearchResponse{}, err
	}
	return response, nil
}

func validateSearchResponse(response openai.SearchResponse, resultLimit int) error {
	if response.Object != "search" || response.Results == nil || len(response.Results) > resultLimit {
		return errors.New("provider returned an invalid search response")
	}
	for _, result := range response.Results {
		parsed, parseErr := url.Parse(result.URL)
		if parseErr != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || len(result.URL) > 4096 || len(result.Title) > 8192 || len(result.Snippet) > 1<<20 || len(result.Date) > 128 || len(result.LastUpdated) > 128 || strings.ContainsAny(result.Date+result.LastUpdated, "\r\n") {
			return errors.New("provider returned an invalid search result")
		}
	}
	return nil
}
