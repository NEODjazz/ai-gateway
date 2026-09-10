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
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	azureIMDSTokenURL   = "http://169.254.169.254/metadata/identity/oauth2/token"
	azureOpenAIResource = "https://cognitiveservices.azure.com/"
	azureTokenMaxBytes  = 32 << 10
)

type azureTokenSource struct {
	explicit         string
	client           *http.Client
	getenv           func(string) string
	now              func() time.Time
	imdsURL          string
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
	TokenType   string          `json:"token_type"`
}

func newAzureTokenSource(explicit string) *azureTokenSource {
	client := newProviderHTTPClient(2 * time.Second)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &azureTokenSource{explicit: explicit, client: client, getenv: os.Getenv, now: time.Now, imdsURL: azureIMDSTokenURL}
}

func (s *azureTokenSource) Token(ctx context.Context) (string, error) {
	if s.explicit != "" {
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
		s.mu.Lock()
		if err == nil {
			s.token, s.expiresAt = token, expiration
			s.refreshAt = azureTokenRefreshAt(now, expiration)
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

func azureTokenRefreshAt(now, expiration time.Time) time.Time {
	advance := expiration.Sub(now) / 2
	if advance > 5*time.Minute {
		advance = 5 * time.Minute
	}
	return expiration.Add(-advance)
}

func (s *azureTokenSource) load(ctx context.Context) (string, time.Time, error) {
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
	query.Set("resource", azureOpenAIResource)
	if clientID := strings.TrimSpace(s.getenv("AZURE_CLIENT_ID")); clientID != "" {
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
	response, err := s.client.Do(request)
	if err != nil {
		return "", time.Time{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", time.Time{}, fmt.Errorf("Azure managed identity endpoint returned status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, azureTokenMaxBytes+1))
	if err != nil || len(data) > azureTokenMaxBytes {
		return "", time.Time{}, errors.New("Azure managed identity response is invalid")
	}
	var value azureTokenResponse
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return "", time.Time{}, errors.New("Azure managed identity response is invalid")
	}
	expiration, err := azureTokenExpiration(value.ExpiresOn)
	if err != nil || value.AccessToken == "" || len(value.AccessToken) > 16<<10 || strings.ContainsAny(value.AccessToken, "\r\n") || !strings.EqualFold(value.TokenType, "Bearer") || !expiration.After(s.now()) {
		return "", time.Time{}, errors.New("Azure managed identity response is invalid")
	}
	return value.AccessToken, expiration, nil
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
	var text string
	if len(raw) == 0 {
		return time.Time{}, errors.New("missing expiration")
	}
	if raw[0] == '"' {
		if json.Unmarshal(raw, &text) != nil {
			return time.Time{}, errors.New("invalid expiration")
		}
	} else {
		text = string(raw)
	}
	seconds, err := strconv.ParseInt(text, 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}, errors.New("invalid expiration")
	}
	return time.Unix(seconds, 0).UTC(), nil
}
