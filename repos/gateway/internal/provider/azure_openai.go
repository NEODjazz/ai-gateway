package provider

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

type azureOpenAITransport struct {
	base        http.RoundTripper
	credential  string
	tokenSource *azureTokenSource
	authType    string
	apiVersion  string
	legacyPath  string
}

func (t azureOpenAITransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	if prefix := t.legacyPath + "/v1/"; t.legacyPath != "" && strings.HasPrefix(cloned.URL.Path, prefix) {
		cloned.URL.Path = t.legacyPath + "/" + strings.TrimPrefix(cloned.URL.Path, prefix)
	}
	if t.apiVersion != "" {
		query := cloned.URL.Query()
		query.Set("api-version", t.apiVersion)
		cloned.URL.RawQuery = query.Encode()
	}
	cloned.Header.Del("Authorization")
	cloned.Header.Del("api-key")
	if t.authType == "entra" {
		token, err := t.tokenSource.Token(request.Context())
		if err != nil {
			return nil, err
		}
		cloned.Header.Set("Authorization", "Bearer "+token)
	} else if t.credential != "" {
		cloned.Header.Set("api-key", t.credential)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(cloned)
}

func NewAzureOpenAI(baseURL, credential string, upstreamStream bool, apiVersion, authType string) OpenAICompatible {
	baseURL = normalizeAzureOpenAIBaseURL(baseURL)
	legacyPath := ""
	if parsed, err := url.Parse(baseURL); err == nil && parsed.Path != "" && !strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/v1") {
		legacyPath = strings.TrimRight(parsed.Path, "/")
	}
	client := NewOpenAICompatible(baseURL, "", upstreamStream)
	client.errorProvider = "azure-openai"
	transport := client.client.Transport
	normalizedAuthType := normalizeAzureAuthType(authType)
	tokenSource := newAzureTokenSource(credential, baseURL)
	client.client.Transport = azureOpenAITransport{
		base: transport, credential: credential, tokenSource: tokenSource, authType: normalizedAuthType, apiVersion: strings.TrimSpace(apiVersion), legacyPath: legacyPath,
	}
	client.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.realtimeURL = func(model string) (*url.URL, error) {
		return azureRealtimeEndpoint(baseURL, strings.TrimSpace(apiVersion), model)
	}
	client.realtimeAuth = func(ctx context.Context) (http.Header, error) {
		return azureRealtimeAuth(ctx, credential, normalizedAuthType, tokenSource)
	}
	return client
}

func azureRealtimeAuth(ctx context.Context, credential, authType string, tokenSource *azureTokenSource) (http.Header, error) {
	header := make(http.Header)
	if authType == "entra" {
		token, tokenErr := tokenSource.Token(ctx)
		if tokenErr != nil {
			return nil, tokenErr
		}
		if strings.TrimSpace(token) == "" {
			return nil, errors.New("azure realtime Entra token is empty")
		}
		header.Set("Authorization", "Bearer "+token)
	} else {
		if strings.TrimSpace(credential) == "" {
			return nil, errors.New("azure realtime API key is required")
		}
		header.Set("api-key", credential)
	}
	return header, nil
}

func azureRealtimeEndpoint(baseURL, apiVersion, model string) (*url.URL, error) {
	endpoint, err := url.Parse(baseURL)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, errors.New("invalid Azure realtime provider URL")
	}
	if _, project := azureFoundryProjectPath(endpoint.Path); project {
		return nil, errors.New("Azure Foundry project URL does not expose the Azure OpenAI realtime endpoint")
	}
	pathPrefix := ""
	if index := strings.LastIndex(endpoint.Path, "/openai/"); index >= 0 {
		pathPrefix = endpoint.Path[:index]
	} else if strings.HasSuffix(endpoint.Path, "/openai") {
		pathPrefix = strings.TrimSuffix(endpoint.Path, "/openai")
	}
	if endpoint.Scheme == "https" {
		endpoint.Scheme = "wss"
	} else {
		endpoint.Scheme = "ws"
	}
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	query := make(url.Values)
	if apiVersion == "" {
		endpoint.Path = pathPrefix + "/openai/v1/realtime"
		query.Set("model", model)
	} else {
		endpoint.Path = pathPrefix + "/openai/realtime"
		query.Set("api-version", apiVersion)
		query.Set("deployment", model)
	}
	endpoint.RawQuery = query.Encode()
	return endpoint, nil
}

func azureRealtimeSupportedBaseURL(baseURL string) bool {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	_, project := azureFoundryProjectPath(parsed.Path)
	return !project
}

func normalizeAzureOpenAIBaseURL(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(value)
	if err != nil {
		return value
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	if strings.Trim(parsed.Path, "/") == "" {
		parsed.Path = "/openai/v1"
	} else if _, projectRoot := azureFoundryProjectPath(parsed.Path); projectRoot && !strings.HasSuffix(parsed.Path, "/openai/v1") {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/openai/v1"
	}
	return strings.TrimRight(parsed.String(), "/")
}

func azureFoundryProjectPath(path string) (string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 3 || parts[0] != "api" || parts[1] != "projects" || parts[2] == "" {
		return "", false
	}
	if len(parts) != 3 && (len(parts) != 5 || parts[3] != "openai" || parts[4] != "v1") {
		return "", false
	}
	return "/api/projects/" + parts[2], true
}

func normalizeAzureAuthType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "api_key"
	}
	return value
}
