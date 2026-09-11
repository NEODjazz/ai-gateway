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

const maxXAIDiscoveredModels = 10000

func discoverXAIModels(ctx context.Context, baseURL, secret string) ([]DiscoveredModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	base, err := url.Parse(baseURL)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, ErrProviderProbeFailed
	}
	path := strings.TrimRight(base.Path, "/")
	if !strings.HasSuffix(path, "/v1") {
		path += "/v1"
	}
	base.Path = path
	base.RawPath = ""
	client := newProviderHTTPClient(10 * time.Second)
	models := map[string]bool{}
	scanned := 0
	for _, catalog := range []string{"models", "embedding-models"} {
		endpoint := *base
		endpoint.Path = base.Path + "/" + catalog
		payload, fetchErr := fetchXAICatalog(ctx, client, endpoint.String(), secret)
		if fetchErr != nil {
			return nil, ErrProviderProbeFailed
		}
		ids, parseErr := parseXAICatalog(catalog, payload)
		if parseErr != nil || scanned > maxXAIDiscoveredModels-len(ids) {
			return nil, ErrProviderProbeFailed
		}
		scanned += len(ids)
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id != "" && len(id) <= 256 {
				models[id] = true
			}
		}
	}
	result := make([]DiscoveredModel, 0, len(models))
	for id := range models {
		result = append(result, DiscoveredModel{ID: id})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func fetchXAICatalog(ctx context.Context, client *http.Client, endpoint, secret string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if secret != "" {
		request.Header.Set("Authorization", "Bearer "+secret)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	closeErr := response.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if response.StatusCode != http.StatusOK || len(payload) > 2<<20 {
		return nil, ErrProviderProbeFailed
	}
	return payload, nil
}

func parseXAICatalog(catalog string, payload []byte) ([]string, error) {
	if catalog == "models" {
		var body struct {
			Data *[]struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(payload, &body); err != nil || body.Data == nil {
			return nil, ErrProviderProbeFailed
		}
		ids := make([]string, 0, len(*body.Data))
		for _, model := range *body.Data {
			ids = append(ids, model.ID)
		}
		return ids, nil
	}
	var body struct {
		Models *[]struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(payload, &body); err != nil || body.Models == nil {
		return nil, ErrProviderProbeFailed
	}
	ids := make([]string, 0, len(*body.Models))
	for _, model := range *body.Models {
		ids = append(ids, model.ID)
	}
	return ids, nil
}
