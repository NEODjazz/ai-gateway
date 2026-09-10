package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	gcpMetadataTokenURL = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"
	gcpTokenMaxBytes    = 32 << 10
)

type gcpTokenSource struct {
	client           *http.Client
	now              func() time.Time
	metadataURL      string
	mu               sync.Mutex
	token            string
	refreshAt        time.Time
	expiresAt        time.Time
	lastErr          error
	retryAt          time.Time
	refreshing       bool
	refreshCompleted chan struct{}
}

type gcpTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

func newGCPTokenSource() *gcpTokenSource {
	client := newProviderHTTPClient(2 * time.Second)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &gcpTokenSource{client: client, now: time.Now, metadataURL: gcpMetadataTokenURL}
}

func (s *gcpTokenSource) Token(ctx context.Context) (string, error) {
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
			s.refreshAt = gcpTokenRefreshAt(now, expiration)
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

func gcpTokenRefreshAt(now, expiration time.Time) time.Time {
	advance := expiration.Sub(now) / 2
	if advance > 5*time.Minute {
		advance = 5 * time.Minute
	}
	return expiration.Add(-advance)
}

func (s *gcpTokenSource) load(ctx context.Context) (string, time.Time, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.metadataURL, nil)
	if err != nil {
		return "", time.Time{}, errors.New("invalid GCP metadata token request")
	}
	request.Header.Set("Metadata-Flavor", "Google")
	response, err := s.client.Do(request)
	if err != nil {
		return "", time.Time{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", time.Time{}, fmt.Errorf("GCP metadata endpoint returned status %d", response.StatusCode)
	}
	if !strings.EqualFold(response.Header.Get("Metadata-Flavor"), "Google") {
		return "", time.Time{}, errors.New("GCP metadata token response is invalid")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, gcpTokenMaxBytes+1))
	if err != nil || len(data) > gcpTokenMaxBytes {
		return "", time.Time{}, errors.New("GCP metadata token response is invalid")
	}
	var value gcpTokenResponse
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF || value.AccessToken == "" || len(value.AccessToken) > 16<<10 || strings.ContainsAny(value.AccessToken, "\r\n") || !strings.EqualFold(value.TokenType, "Bearer") || value.ExpiresIn <= 0 || value.ExpiresIn > int64((24*time.Hour)/time.Second) {
		return "", time.Time{}, errors.New("GCP metadata token response is invalid")
	}
	return value.AccessToken, s.now().Add(time.Duration(value.ExpiresIn) * time.Second), nil
}
