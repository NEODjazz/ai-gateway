package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type ProviderProbe struct {
	ProviderID string `json:"provider_id"`
	Status     string `json:"status"`
	LatencyMS  int64  `json:"latency_ms"`
	ModelCount int    `json:"model_count"`
}

type DiscoveredModel struct {
	ID string `json:"id"`
}

type ProviderDiscoveryController interface {
	TestProvider(context.Context, string, string) (ProviderProbe, error)
	DiscoverProviderModels(context.Context, string, string) ([]DiscoveredModel, error)
}

var ErrProviderProbeFailed = errors.New("provider connection test failed")

func (r *Router) TestProvider(ctx context.Context, providerID, credentialID string) (ProviderProbe, error) {
	started := time.Now()
	models, err := r.DiscoverProviderModels(ctx, providerID, credentialID)
	probe := ProviderProbe{ProviderID: strings.TrimSpace(providerID), Status: "available", LatencyMS: time.Since(started).Milliseconds(), ModelCount: len(models)}
	if err != nil {
		probe.Status = "unavailable"
		return probe, err
	}
	return probe, nil
}

func (r *Router) DiscoverProviderModels(ctx context.Context, providerID, credentialID string) ([]DiscoveredModel, error) {
	if err := r.refreshControlPlane(ctx); err != nil {
		return nil, err
	}
	managed, found := r.managedProvider(providerID)
	if !found {
		return nil, ErrProviderNotFound
	}
	if !managed.Enabled {
		return nil, ErrProviderProbeFailed
	}
	if managed.Type == "demo" {
		return []DiscoveredModel{{ID: "demo-model"}}, nil
	}
	secret, err := r.providerCredentialSecret(managed.ID, credentialID)
	if err != nil {
		return nil, err
	}
	endpoint, err := discoveryURL(managed)
	if err != nil {
		return nil, ErrProviderProbeFailed
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, ErrProviderProbeFailed
	}
	request.Header.Set("Accept", "application/json")
	if secret != "" {
		if managed.Type == "anthropic" {
			request.Header.Set("x-api-key", secret)
			request.Header.Set("anthropic-version", "2023-06-01")
		} else {
			request.Header.Set("Authorization", "Bearer "+secret)
		}
	}
	client := newProviderHTTPClient(10 * time.Second)
	response, err := client.Do(request)
	if err != nil {
		return nil, ErrProviderProbeFailed
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return nil, ErrProviderProbeFailed
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(payload) > 2<<20 {
		return nil, ErrProviderProbeFailed
	}
	models, err := parseDiscoveredModels(managed.Type, payload)
	if err != nil {
		return nil, ErrProviderProbeFailed
	}
	return models, nil
}

func discoveryURL(managed ManagedProvider) (string, error) {
	base, err := url.Parse(managed.BaseURL)
	if err != nil {
		return "", err
	}
	if managed.Type == "ollama" {
		base.Path = strings.TrimRight(base.Path, "/") + "/api/tags"
	} else {
		path := strings.TrimRight(base.Path, "/")
		if !strings.HasSuffix(path, "/v1") {
			path += "/v1"
		}
		base.Path = path + "/models"
	}
	base.RawQuery = ""
	base.Fragment = ""
	return base.String(), nil
}

func parseDiscoveredModels(providerType string, payload []byte) ([]DiscoveredModel, error) {
	ids := []string{}
	if providerType == "ollama" {
		var body struct {
			Models []struct {
				Name  string `json:"name"`
				Model string `json:"model"`
			} `json:"models"`
		}
		if err := json.Unmarshal(payload, &body); err != nil {
			return nil, err
		}
		for _, item := range body.Models {
			id := item.Name
			if id == "" {
				id = item.Model
			}
			ids = append(ids, id)
		}
	} else {
		var body struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(payload, &body); err != nil {
			return nil, err
		}
		for _, item := range body.Data {
			ids = append(ids, item.ID)
		}
	}
	seen := map[string]bool{}
	result := make([]DiscoveredModel, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || len(id) > 256 || seen[id] {
			continue
		}
		seen[id] = true
		result = append(result, DiscoveredModel{ID: id})
	}
	if len(result) > 10000 {
		return nil, fmt.Errorf("too many models")
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
