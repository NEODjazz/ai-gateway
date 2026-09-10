package provider

import (
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
	transport := client.client.Transport
	client.client.Transport = azureOpenAITransport{
		base: transport, credential: credential, tokenSource: newAzureTokenSource(credential, baseURL), authType: normalizeAzureAuthType(authType), apiVersion: strings.TrimSpace(apiVersion), legacyPath: legacyPath,
	}
	client.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client
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
	}
	return strings.TrimRight(parsed.String(), "/")
}

func normalizeAzureAuthType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "api_key"
	}
	return value
}
