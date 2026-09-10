package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
)

func discoverGeminiModels(ctx context.Context, baseURL, secret string, authTypes ...string) ([]DiscoveredModel, error) {
	authType := "api_key"
	if len(authTypes) > 0 {
		authType = authTypes[0]
	}
	gemini := NewGeminiWithAuth(baseURL, secret, false, authType)
	return discoverGeminiModelsWithClient(ctx, gemini)
}

func discoverGeminiModelsWithClient(ctx context.Context, gemini Gemini) ([]DiscoveredModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	base, err := url.Parse(geminiBaseURL(gemini.baseURL) + "/models")
	if err != nil || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, ErrProviderProbeFailed
	}
	client := gemini.client
	client.Timeout = 10 * time.Second
	models := map[string]bool{}
	pages := map[string]bool{}
	token := ""
	scanned := 0
	for range 100 {
		query := url.Values{"pageSize": []string{"1000"}}
		if token != "" {
			query.Set("pageToken", token)
		}
		base.RawQuery = query.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
		if err != nil {
			return nil, ErrProviderProbeFailed
		}
		if err := gemini.authorize(req); err != nil {
			return nil, ErrProviderProbeFailed
		}
		req.Header.Set("Accept", "application/json")
		response, err := client.Do(req)
		if err != nil {
			return nil, ErrProviderProbeFailed
		}
		payload, readErr := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || response.StatusCode != 200 || len(payload) > 2<<20 {
			return nil, ErrProviderProbeFailed
		}
		var body struct {
			Models []struct {
				Name    string   `json:"name"`
				Methods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			Next string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(payload, &body); err != nil {
			return nil, ErrProviderProbeFailed
		}
		scanned += len(body.Models)
		if scanned > 10000 {
			return nil, ErrProviderProbeFailed
		}
		for _, model := range body.Models {
			name := strings.TrimPrefix(model.Name, "models/")
			if name != "" && len(name) <= 256 && (slices.Contains(model.Methods, "generateContent") || slices.Contains(model.Methods, "embedContent") || slices.Contains(model.Methods, "batchEmbedContents")) {
				models[name] = true
			}
		}
		if body.Next == "" {
			result := make([]DiscoveredModel, 0, len(models))
			for name := range models {
				result = append(result, DiscoveredModel{ID: name})
			}
			sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
			return result, nil
		}
		if len(body.Next) > 4096 || pages[body.Next] {
			return nil, ErrProviderProbeFailed
		}
		pages[body.Next] = true
		token = body.Next
	}
	return nil, ErrProviderProbeFailed
}
