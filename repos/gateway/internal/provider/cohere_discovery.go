package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

func discoverCohereModels(ctx context.Context, managed ManagedProvider, secret string) ([]DiscoveredModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	endpoint, err := discoveryURL(managed)
	if err != nil {
		return nil, ErrProviderProbeFailed
	}
	base, err := url.Parse(endpoint)
	if err != nil || base.Host == "" || base.User != nil || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, ErrProviderProbeFailed
	}
	client := newProviderHTTPClient(10 * time.Second)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	seenModels := make(map[string]bool)
	seenTokens := make(map[string]bool)
	pageToken := ""
	for page := 0; page < 64; page++ {
		query := url.Values{"page_size": {"1000"}}
		if pageToken != "" {
			query.Set("page_token", pageToken)
		}
		base.RawQuery = query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
		if err != nil {
			return nil, ErrProviderProbeFailed
		}
		request.Header.Set("Accept", "application/json")
		if secret != "" {
			request.Header.Set("Authorization", "Bearer "+secret)
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, ErrProviderProbeFailed
		}
		payload, readErr := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		closeErr := response.Body.Close()
		if response.StatusCode != http.StatusOK || readErr != nil || closeErr != nil || len(payload) > 2<<20 {
			return nil, ErrProviderProbeFailed
		}
		models, err := parseDiscoveredModels("cohere", payload)
		if err != nil {
			return nil, ErrProviderProbeFailed
		}
		var body struct {
			NextPageToken string `json:"next_page_token"`
		}
		if json.Unmarshal(payload, &body) != nil {
			return nil, ErrProviderProbeFailed
		}
		for _, model := range models {
			seenModels[model.ID] = true
		}
		if len(seenModels) > 10000 {
			return nil, ErrProviderProbeFailed
		}
		if body.NextPageToken == "" {
			result := make([]DiscoveredModel, 0, len(seenModels))
			for id := range seenModels {
				result = append(result, DiscoveredModel{ID: id})
			}
			sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
			return result, nil
		}
		if len(body.NextPageToken) > 4096 || strings.TrimSpace(body.NextPageToken) == "" || seenTokens[body.NextPageToken] {
			return nil, ErrProviderProbeFailed
		}
		seenTokens[body.NextPageToken] = true
		pageToken = body.NextPageToken
	}
	return nil, ErrProviderProbeFailed
}
