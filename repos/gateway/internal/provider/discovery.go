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
	if managed.Type == "vertex-gemini" {
		return nil, ErrProviderProbeFailed
	}
	secret, err := r.providerCredentialSecret(managed.ID, credentialID)
	if err != nil {
		return nil, err
	}
	if managed.Type == "azure-openai" {
		if parsed, parseErr := url.Parse(managed.BaseURL); parseErr == nil {
			if projectPath, project := azureFoundryProjectPath(parsed.Path); project {
				return discoverAzureFoundryProjectModels(ctx, managed, secret, projectPath)
			}
		}
	}
	if managed.Type == "gemini" {
		return discoverGeminiModels(ctx, managed.BaseURL, secret, managed.AuthType)
	}
	if managed.Type == "anthropic" {
		return discoverAnthropicModels(ctx, managed.BaseURL, secret)
	}
	if managed.Type == "xai" {
		return discoverXAIModels(ctx, managed.BaseURL, secret)
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
	if managed.Type == "bedrock" && managed.AuthType == "aws_sigv4" {
		credential, credentialErr := r.awsCredentialSource(managed.ID, credentialID, secret, managed.Region).Credential(ctx)
		if credentialErr != nil || signAWSRequest(request, nil, credential, managed.Region, "bedrock", time.Now()) != nil {
			return nil, ErrProviderProbeFailed
		}
	} else if managed.Type == "azure-openai" && normalizeAzureAuthType(managed.AuthType) == "entra" {
		token, tokenErr := newAzureTokenSource(secret, managed.BaseURL).Token(ctx)
		if tokenErr != nil {
			return nil, ErrProviderProbeFailed
		}
		request.Header.Set("Authorization", "Bearer "+token)
	} else if secret != "" {
		if managed.Type == "anthropic" {
			request.Header.Set("x-api-key", secret)
			request.Header.Set("anthropic-version", "2023-06-01")
		} else if managed.Type == "azure-openai" {
			request.Header.Set("api-key", secret)
		} else {
			request.Header.Set("Authorization", "Bearer "+secret)
		}
	}
	client := newProviderHTTPClient(10 * time.Second)
	if managed.Type == "azure-openai" || managed.Type == "bedrock" {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
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

func discoverAzureFoundryProjectModels(ctx context.Context, managed ManagedProvider, secret, projectPath string) ([]DiscoveredModel, error) {
	endpoint, err := url.Parse(managed.BaseURL)
	if err != nil {
		return nil, ErrProviderProbeFailed
	}
	endpoint.Path = projectPath + "/deployments"
	endpoint.RawQuery = "api-version=v1"
	endpoint.Fragment = ""
	client := newProviderHTTPClient(10 * time.Second)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	tokenSource := newAzureTokenSource(secret, managed.BaseURL)
	seenURLs := make(map[string]bool)
	seenModels := make(map[string]bool)
	models := make([]DiscoveredModel, 0)
	for page := 0; page < 64; page++ {
		if seenURLs[endpoint.String()] {
			return nil, ErrProviderProbeFailed
		}
		seenURLs[endpoint.String()] = true
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return nil, ErrProviderProbeFailed
		}
		request.Header.Set("Accept", "application/json")
		if normalizeAzureAuthType(managed.AuthType) == "entra" {
			token, tokenErr := tokenSource.Token(ctx)
			if tokenErr != nil {
				return nil, ErrProviderProbeFailed
			}
			request.Header.Set("Authorization", "Bearer "+token)
		} else if secret != "" {
			request.Header.Set("api-key", secret)
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, ErrProviderProbeFailed
		}
		payload, readErr := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		closeErr := response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 || readErr != nil || closeErr != nil || len(payload) > 2<<20 {
			return nil, ErrProviderProbeFailed
		}
		var body struct {
			Value *[]struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"value"`
			NextLink string `json:"nextLink"`
		}
		if json.Unmarshal(payload, &body) != nil || body.Value == nil {
			return nil, ErrProviderProbeFailed
		}
		for _, item := range *body.Value {
			name := strings.TrimSpace(item.Name)
			if item.Type != "ModelDeployment" || name == "" || len(name) > 256 {
				return nil, ErrProviderProbeFailed
			}
			if !seenModels[name] {
				models = append(models, DiscoveredModel{ID: name})
				seenModels[name] = true
			}
		}
		if len(models) > 10000 {
			return nil, ErrProviderProbeFailed
		}
		if body.NextLink == "" {
			sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
			return models, nil
		}
		if len(body.NextLink) > 4096 {
			return nil, ErrProviderProbeFailed
		}
		next, parseErr := url.Parse(body.NextLink)
		if parseErr != nil {
			return nil, ErrProviderProbeFailed
		}
		next = endpoint.ResolveReference(next)
		if next.Scheme != endpoint.Scheme || !strings.EqualFold(next.Host, endpoint.Host) || next.User != nil || next.Path != projectPath+"/deployments" || next.Fragment != "" {
			return nil, ErrProviderProbeFailed
		}
		query := next.Query()
		query.Set("api-version", "v1")
		next.RawQuery = query.Encode()
		endpoint = next
	}
	return nil, ErrProviderProbeFailed
}

func discoveryURL(managed ManagedProvider) (string, error) {
	if managed.Type == "azure-openai" {
		return azureOpenAIDiscoveryURL(managed)
	}
	baseURL := managed.BaseURL
	if managed.Type == "ollama" {
		baseURL = normalizeOllamaBaseURL(baseURL)
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	if managed.Type == "ollama" {
		base.Path = strings.TrimRight(base.Path, "/") + "/api/tags"
	} else if managed.Type == "bedrock" {
		if strings.HasPrefix(base.Hostname(), "bedrock-runtime.") {
			base.Host = strings.Replace(base.Host, "bedrock-runtime.", "bedrock.", 1)
		}
		base.Path = strings.TrimRight(base.Path, "/") + "/foundation-models"
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

func azureOpenAIDiscoveryURL(managed ManagedProvider) (string, error) {
	base, err := url.Parse(managed.BaseURL)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(base.Path, "/")
	if index := strings.Index(path, "/openai/deployments/"); index >= 0 {
		path = path[:index] + "/openai"
	} else {
		normalized, parseErr := url.Parse(normalizeAzureOpenAIBaseURL(managed.BaseURL))
		if parseErr != nil {
			return "", parseErr
		}
		path = strings.TrimRight(normalized.Path, "/")
	}
	base.Path = path + "/models"
	query := url.Values{}
	if managed.APIVersion != "" {
		query.Set("api-version", managed.APIVersion)
	}
	base.RawQuery = query.Encode()
	base.Fragment = ""
	return base.String(), nil
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
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
	} else if providerType == "cohere" {
		var body struct {
			Models []struct {
				Name       string   `json:"name"`
				Deprecated bool     `json:"is_deprecated"`
				Endpoints  []string `json:"endpoints"`
			} `json:"models"`
		}
		if err := json.Unmarshal(payload, &body); err != nil {
			return nil, err
		}
		for _, item := range body.Models {
			if item.Deprecated || (!containsString(item.Endpoints, "chat") && !containsString(item.Endpoints, "rerank") && !containsString(item.Endpoints, "embed")) {
				continue
			}
			ids = append(ids, item.Name)
		}
	} else if providerType == "bedrock" {
		var body struct {
			Models *[]struct {
				ID string `json:"modelId"`
			} `json:"modelSummaries"`
		}
		if err := json.Unmarshal(payload, &body); err != nil {
			return nil, err
		}
		if body.Models == nil {
			return nil, errors.New("Bedrock discovery response omitted modelSummaries")
		}
		for _, item := range *body.Models {
			ids = append(ids, item.ID)
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
