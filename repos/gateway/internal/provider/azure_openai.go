package provider

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"ai-gateway-gateway/internal/azureurl"
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
		suffix := strings.TrimPrefix(cloned.URL.Path, prefix)
		if deploymentIndex := strings.LastIndex(t.legacyPath, "/openai/deployments/"); deploymentIndex >= 0 && (suffix == "responses" || strings.HasPrefix(suffix, "responses/")) {
			cloned.URL.Path = t.legacyPath[:deploymentIndex] + "/openai/" + suffix
		} else {
			cloned.URL.Path = t.legacyPath + "/" + suffix
		}
		cloned.URL.RawPath = ""
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
	} else if strings.TrimSpace(t.credential) == "" {
		return nil, errors.New("Azure OpenAI API key is required")
	} else {
		cloned.Header.Set("api-key", t.credential)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(cloned)
}

func NewAzureOpenAI(baseURL, credential string, upstreamStream bool, apiVersion, authType string, azureCloud ...string) OpenAICompatible {
	baseURL = normalizeAzureOpenAIBaseURL(baseURL)
	legacyPath := ""
	if parsed, err := url.Parse(baseURL); err == nil {
		path := strings.TrimRight(parsed.Path, "/")
		if path != "" && !strings.HasSuffix(path, "/v1") && (strings.Contains(path, "/openai/deployments/") || apiVersion != "" && apiVersion != "preview") {
			legacyPath = path
		}
	}
	client := NewOpenAICompatible(baseURL, "", upstreamStream)
	client.errorProvider = "azure-openai"
	client.exactChatUsage = true
	client.exactResponseUsage = true
	client.exactEmbeddingUsage = true
	transport := client.client.Transport
	normalizedAuthType := normalizeAzureAuthType(authType)
	cloud, audience := "", ""
	if len(azureCloud) > 0 {
		cloud = azureCloud[0]
	}
	if len(azureCloud) > 1 {
		audience = azureCloud[1]
	}
	tokenSource := newAzureTokenSourceWithPolicy(credential, baseURL, cloud, audience)
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

func azureManagedDeploymentBaseURL(baseURL, apiVersion string, deployment ModelDeployment) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || !azureurl.ValidPath(parsed) {
		return "", ErrInvalidDeployment
	}
	if apiVersion == "" || apiVersion == "preview" {
		return baseURL, nil
	}
	path := strings.TrimRight(parsed.Path, "/")
	if index := strings.LastIndex(path, "/openai/deployments/"); index >= 0 {
		if !validAzureDeploymentPathSegment(path[index+len("/openai/deployments/"):]) {
			return "", ErrInvalidDeployment
		}
		return baseURL, nil
	}
	if strings.HasSuffix(path, "/openai/v1") {
		path = strings.TrimSuffix(path, "/openai/v1")
	} else if strings.HasSuffix(path, "/openai") {
		path = strings.TrimSuffix(path, "/openai")
	} else if strings.Contains(path, "/openai/") || strings.HasSuffix(path, "/openai/deployments") {
		return "", ErrInvalidDeployment
	}
	model := deployment.UpstreamModel
	if model == "" {
		if len(deployment.Models) != 1 {
			return "", ErrInvalidDeployment
		}
		model = deployment.Models[0]
	}
	if !validAzureDeploymentPathSegment(model) {
		return "", ErrInvalidDeployment
	}
	parsed.Path = path + "/openai/deployments/" + model
	parsed.RawPath = ""
	return parsed.String(), nil
}

func validAzureDeploymentPathSegment(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 256 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' && char != '.' {
			return false
		}
	}
	return true
}

func azureFoundryProjectPath(path string) (string, bool) {
	return azureurl.ProjectPath(path)
}

func normalizeAzureAuthType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "api_key"
	}
	return value
}
