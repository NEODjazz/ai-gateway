package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	azureIMDSTokenURL              = "http://169.254.169.254/metadata/identity/oauth2/token"
	azureAuthorityURL              = "https://login.microsoftonline.com"
	azureOpenAIResource            = "https://cognitiveservices.azure.com/"
	azureOpenAIScope               = azureOpenAIResource + ".default"
	azureFoundryResource           = "https://ai.azure.com/"
	azureGovernmentAuthority       = "https://login.microsoftonline.us"
	azureGovernmentResource        = "https://cognitiveservices.azure.us/"
	azureGovernmentFoundryResource = "https://ai.azure.us/"
	azureChinaAuthority            = "https://login.chinacloudapi.cn"
	azureChinaResource             = "https://cognitiveservices.azure.cn/"
	azureTokenMaxBytes             = 32 << 10
	azureAssertionMaxBytes         = 64 << 10
	azureClientSecretMaxBytes      = 8 << 10
)

type azureTokenSource struct {
	explicit         string
	client           *http.Client
	getenv           func(string) string
	now              func() time.Time
	imdsURL          string
	authorityBaseURL string
	resource         string
	scope            string
	mu               sync.Mutex
	token            string
	refreshAt        time.Time
	expiresAt        time.Time
	lastErr          error
	retryAt          time.Time
	refreshing       bool
	refreshCompleted chan struct{}
}

type azureTokenResponse struct {
	AccessToken string          `json:"access_token"`
	ExpiresOn   json.RawMessage `json:"expires_on"`
	ExpiresIn   json.RawMessage `json:"expires_in"`
	TokenType   string          `json:"token_type"`
}

func newAzureTokenSource(explicit string, providerBaseURL ...string) *azureTokenSource {
	client := newProviderHTTPClient(2 * time.Second)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	authority, resource := azureIdentityEndpoints(providerBaseURL...)
	return &azureTokenSource{explicit: explicit, client: client, getenv: os.Getenv, now: time.Now, imdsURL: azureIMDSTokenURL, authorityBaseURL: authority, resource: resource, scope: resource + ".default"}
}

func newAzureTokenSourceWithCloud(explicit, providerBaseURL, cloud string) *azureTokenSource {
	source := newAzureTokenSource(explicit, providerBaseURL)
	if cloud == "" {
		return source
	}
	project := source.resource == azureFoundryResource || source.resource == azureGovernmentFoundryResource
	switch cloud {
	case "public":
		source.authorityBaseURL = azureAuthorityURL
		if project {
			source.resource = azureFoundryResource
		} else {
			source.resource = azureOpenAIResource
		}
	case "usgov":
		source.authorityBaseURL = azureGovernmentAuthority
		if project {
			source.resource = azureGovernmentFoundryResource
		} else {
			source.resource = azureGovernmentResource
		}
	case "china":
		source.authorityBaseURL = azureChinaAuthority
		source.resource = azureChinaResource
	}
	source.scope = source.resource + ".default"
	return source
}

func newAzureTokenSourceWithPolicy(explicit, providerBaseURL, cloud, audience string) *azureTokenSource {
	source := newAzureTokenSourceWithCloud(explicit, providerBaseURL, cloud)
	if audience != "cognitive" && audience != "foundry" {
		return source
	}
	switch source.authorityBaseURL {
	case azureGovernmentAuthority:
		if audience == "foundry" {
			source.resource = azureGovernmentFoundryResource
		} else {
			source.resource = azureGovernmentResource
		}
	case azureChinaAuthority:
		source.resource = azureChinaResource
	default:
		if audience == "foundry" {
			source.resource = azureFoundryResource
		} else {
			source.resource = azureOpenAIResource
		}
	}
	source.scope = source.resource + ".default"
	return source
}

func azureIdentityEndpoints(providerBaseURL ...string) (string, string) {
	if len(providerBaseURL) > 0 {
		if parsed, err := url.Parse(providerBaseURL[0]); err == nil {
			host := strings.ToLower(parsed.Hostname())
			if strings.HasSuffix(host, ".services.ai.azure.com") {
				return azureAuthorityURL, azureFoundryResource
			}
			if strings.HasSuffix(host, ".services.ai.azure.us") {
				return azureGovernmentAuthority, azureGovernmentFoundryResource
			}
			if strings.HasSuffix(host, ".openai.azure.us") || strings.HasSuffix(host, ".cognitiveservices.azure.us") {
				return azureGovernmentAuthority, azureGovernmentResource
			}
			if strings.HasSuffix(host, ".openai.azure.cn") || strings.HasSuffix(host, ".cognitiveservices.azure.cn") {
				return azureChinaAuthority, azureChinaResource
			}
			if _, project := azureFoundryProjectPath(parsed.Path); project {
				return azureAuthorityURL, azureFoundryResource
			}
		}
	}
	return azureAuthorityURL, azureOpenAIResource
}

func (s *azureTokenSource) Token(ctx context.Context) (string, error) {
	if s.explicit != "" {
		if !validAzureBearerToken(s.explicit) {
			return "", errors.New("invalid explicit Azure Entra token")
		}
		return s.explicit, nil
	}
	for {
		now := s.now()
		s.mu.Lock()
		if s.token != "" && now.Before(s.refreshAt) {
			token := s.token
			s.mu.Unlock()
			return token, nil
		}
		if s.lastErr != nil && now.Before(s.retryAt) {
			if s.token != "" && now.Before(s.expiresAt) {
				token := s.token
				s.mu.Unlock()
				return token, nil
			}
			err := s.lastErr
			s.mu.Unlock()
			return "", err
		}
		if s.refreshing {
			done := s.refreshCompleted
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-done:
				continue
			}
		}
		s.refreshing = true
		s.refreshCompleted = make(chan struct{})
		done := s.refreshCompleted
		s.mu.Unlock()
		token, expiration, err := s.load(ctx)
		now = s.now()
		s.mu.Lock()
		if err == nil {
			s.token, s.expiresAt = token, expiration
			s.refreshAt = azureTokenRefreshAt(now, expiration)
			s.lastErr, s.retryAt = nil, time.Time{}
		} else if ctx.Err() != nil {
			s.lastErr, s.retryAt = nil, time.Time{}
		} else {
			s.lastErr, s.retryAt = err, now.Add(time.Second)
			if s.token != "" && now.Before(s.expiresAt) {
				token, err = s.token, nil
			}
		}
		s.refreshing = false
		close(done)
		s.mu.Unlock()
		return token, err
	}
}

func (s *azureTokenSource) invalidate(token string) {
	if s.explicit != "" || token == "" {
		return
	}
	s.mu.Lock()
	if s.token == token {
		s.token = ""
		s.refreshAt = time.Time{}
		s.expiresAt = time.Time{}
	}
	s.mu.Unlock()
}

func azureTokenRefreshAt(now, expiration time.Time) time.Time {
	advance := expiration.Sub(now) / 2
	if advance > 5*time.Minute {
		advance = 5 * time.Minute
	}
	return expiration.Add(-advance)
}

func (s *azureTokenSource) load(ctx context.Context) (string, time.Time, error) {
	tenantID := strings.TrimSpace(s.getenv("AZURE_TENANT_ID"))
	clientID := strings.TrimSpace(s.getenv("AZURE_CLIENT_ID"))
	clientSecret := s.getenv("AZURE_CLIENT_SECRET")
	tokenFile := strings.TrimSpace(s.getenv("AZURE_FEDERATED_TOKEN_FILE"))
	if clientSecret != "" {
		if tenantID == "" || clientID == "" || tokenFile != "" {
			return "", time.Time{}, errors.New("invalid Azure client secret configuration")
		}
		return s.loadClientSecret(ctx, tenantID, clientID, clientSecret)
	}
	if tenantID != "" || tokenFile != "" {
		if tenantID == "" || clientID == "" || tokenFile == "" {
			return "", time.Time{}, errors.New("incomplete Azure federated workload identity configuration")
		}
		return s.loadFederated(ctx, tenantID, clientID, tokenFile)
	}
	endpoint := strings.TrimSpace(s.getenv("IDENTITY_ENDPOINT"))
	header := s.getenv("IDENTITY_HEADER")
	apiVersion := "2019-08-01"
	if endpoint == "" && header == "" {
		endpoint = s.imdsURL
		apiVersion = "2018-02-01"
	} else if endpoint == "" || header == "" || !validAzureIdentityEndpoint(endpoint) {
		return "", time.Time{}, errors.New("invalid Azure managed identity configuration")
	}
	if len(header) > 8<<10 || strings.ContainsAny(header, "\r\n") {
		return "", time.Time{}, errors.New("invalid Azure managed identity configuration")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", time.Time{}, errors.New("invalid Azure managed identity endpoint")
	}
	query := parsed.Query()
	query.Set("api-version", apiVersion)
	query.Set("resource", s.resource)
	if clientID != "" {
		if len(clientID) > 128 || strings.ContainsAny(clientID, "\x00\r\n") {
			return "", time.Time{}, errors.New("invalid Azure managed identity client ID")
		}
		query.Set("client_id", clientID)
	}
	parsed.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", time.Time{}, errors.New("invalid Azure managed identity request")
	}
	if header != "" {
		request.Header.Set("X-IDENTITY-HEADER", header)
	} else {
		request.Header.Set("Metadata", "true")
	}
	return s.fetchToken(request, false)
}

func (s *azureTokenSource) fetchToken(request *http.Request, useExpiresIn bool) (string, time.Time, error) {
	response, err := s.client.Do(request)
	if err != nil {
		return "", time.Time{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", time.Time{}, fmt.Errorf("Azure token endpoint returned status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, azureTokenMaxBytes+1))
	if err != nil || len(data) > azureTokenMaxBytes {
		return "", time.Time{}, errors.New("Azure token response is invalid")
	}
	var value azureTokenResponse
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return "", time.Time{}, errors.New("Azure token response is invalid")
	}
	expiration, err := azureTokenExpiration(value.ExpiresOn)
	if useExpiresIn {
		duration, durationErr := azureTokenLifetime(value.ExpiresIn)
		if durationErr != nil {
			err = durationErr
		} else {
			expiration = s.now().Add(duration)
			err = nil
		}
	}
	if err != nil || !validAzureBearerToken(value.AccessToken) || !strings.EqualFold(value.TokenType, "Bearer") || !expiration.After(s.now()) {
		return "", time.Time{}, errors.New("Azure token response is invalid")
	}
	return value.AccessToken, expiration, nil
}

func validAzureBearerToken(value string) bool {
	return value != "" && len(value) <= 16<<10 && !strings.ContainsAny(value, " \t\r\n\x00")
}

func (s *azureTokenSource) loadFederated(ctx context.Context, tenantID, clientID, tokenFile string) (string, time.Time, error) {
	if !validAzureIdentifier(tenantID) || !validAzureIdentifier(clientID) || !filepath.IsAbs(tokenFile) {
		return "", time.Time{}, errors.New("invalid Azure federated workload identity configuration")
	}
	assertion, err := readBoundedCredentialFile(tokenFile, azureAssertionMaxBytes)
	if err != nil {
		return "", time.Time{}, errors.New("invalid Azure federated token file")
	}
	assertion = []byte(strings.TrimSpace(string(assertion)))
	if len(assertion) == 0 || strings.ContainsAny(string(assertion), "\r\n") {
		return "", time.Time{}, errors.New("invalid Azure federated token file")
	}
	form := url.Values{
		"client_id":             {clientID},
		"scope":                 {s.scope},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {string(assertion)},
		"grant_type":            {"client_credentials"},
	}
	endpoint := strings.TrimRight(s.authorityBaseURL, "/") + "/" + url.PathEscape(tenantID) + "/oauth2/v2.0/token"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, errors.New("invalid Azure federated token request")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return s.fetchToken(request, true)
}

func (s *azureTokenSource) loadClientSecret(ctx context.Context, tenantID, clientID, clientSecret string) (string, time.Time, error) {
	if !validAzureIdentifier(tenantID) || !validAzureIdentifier(clientID) || len(clientSecret) > azureClientSecretMaxBytes || strings.TrimSpace(clientSecret) == "" || strings.ContainsAny(clientSecret, "\x00\r\n") {
		return "", time.Time{}, errors.New("invalid Azure client secret configuration")
	}
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"scope":         {s.scope},
		"grant_type":    {"client_credentials"},
	}
	endpoint := strings.TrimRight(s.authorityBaseURL, "/") + "/" + url.PathEscape(tenantID) + "/oauth2/v2.0/token"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, errors.New("invalid Azure client secret token request")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return s.fetchToken(request, true)
}

func validAzureIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '.' {
			return false
		}
	}
	return true
}

func validAzureIdentityEndpoint(value string) bool {
	if len(value) > 2048 {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Fragment != "" || parsed.Host == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsLinkLocalUnicast())
}

func azureTokenExpiration(raw json.RawMessage) (time.Time, error) {
	seconds, err := azureTokenInteger(raw)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(seconds, 0).UTC(), nil
}

func azureTokenLifetime(raw json.RawMessage) (time.Duration, error) {
	seconds, err := azureTokenInteger(raw)
	if err != nil || seconds > int64((24*time.Hour)/time.Second) {
		return 0, errors.New("invalid token lifetime")
	}
	return time.Duration(seconds) * time.Second, nil
}

func azureTokenInteger(raw json.RawMessage) (int64, error) {
	var text string
	if len(raw) == 0 {
		return 0, errors.New("missing expiration")
	}
	if raw[0] == '"' {
		if json.Unmarshal(raw, &text) != nil {
			return 0, errors.New("invalid expiration")
		}
	} else {
		text = string(raw)
	}
	seconds, err := strconv.ParseInt(text, 10, 64)
	if err != nil || seconds <= 0 {
		return 0, errors.New("invalid expiration")
	}
	return seconds, nil
}
