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

func discoverAnthropicModels(ctx context.Context, baseURL, secret string) ([]DiscoveredModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	base, err := url.Parse(baseURL)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, ErrProviderProbeFailed
	}
	base.Path = strings.TrimRight(base.Path, "/")
	if !strings.HasSuffix(base.Path, "/v1") {
		base.Path += "/v1"
	}
	base.Path += "/models"
	base.RawPath = ""
	client := newProviderHTTPClient(10 * time.Second)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	models := map[string]bool{}
	cursors := map[string]bool{}
	cursor := ""
	scanned := 0
	for range 100 {
		query := url.Values{"limit": []string{"1000"}}
		if cursor != "" {
			query.Set("after_id", cursor)
		}
		base.RawQuery = query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
		if err != nil {
			return nil, ErrProviderProbeFailed
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("x-api-key", secret)
		request.Header.Set("anthropic-version", "2023-06-01")
		response, err := client.Do(request)
		if err != nil {
			return nil, ErrProviderProbeFailed
		}
		payload, readErr := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK || len(payload) > 2<<20 {
			return nil, ErrProviderProbeFailed
		}
		var body struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := json.Unmarshal(payload, &body); err != nil {
			return nil, ErrProviderProbeFailed
		}
		scanned += len(body.Data)
		if scanned > 10000 {
			return nil, ErrProviderProbeFailed
		}
		for _, model := range body.Data {
			id := strings.TrimSpace(model.ID)
			if id != "" && len(id) <= 256 {
				models[id] = true
			}
		}
		if !body.HasMore {
			result := make([]DiscoveredModel, 0, len(models))
			for id := range models {
				result = append(result, DiscoveredModel{ID: id})
			}
			sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
			return result, nil
		}
		if len(body.Data) == 0 || body.LastID == "" || len(body.LastID) > 256 || cursors[body.LastID] {
			return nil, ErrProviderProbeFailed
		}
		cursors[body.LastID] = true
		cursor = body.LastID
	}
	return nil, ErrProviderProbeFailed
}
