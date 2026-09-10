package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	awsECSCredentialBaseURL = "http://169.254.170.2"
	awsIMDSBaseURL          = "http://169.254.169.254"
	awsCredentialMaxBytes   = 32 << 10
)

type awsCredentialSource struct {
	explicit         string
	client           *http.Client
	getenv           func(string) string
	now              func() time.Time
	ecsBaseURL       string
	imdsBaseURL      string
	mu               sync.Mutex
	cached           awsCredential
	cacheValid       bool
	refreshAt        time.Time
	expiresAt        time.Time
	lastErr          error
	retryAt          time.Time
	refreshing       bool
	refreshCompleted chan struct{}
}

type managedAWSCredentialSource struct {
	explicit string
	source   *awsCredentialSource
}

type awsCredentialRegistry struct {
	mu      sync.Mutex
	current map[string]managedAWSCredentialSource
}

type awsRoleCredential struct {
	AccessKeyID     string    `json:"AccessKeyId"`
	SecretAccessKey string    `json:"SecretAccessKey"`
	Token           string    `json:"Token"`
	Expiration      time.Time `json:"Expiration"`
}

func newAWSCredentialSource(explicit string) *awsCredentialSource {
	client := newProviderHTTPClient(2 * time.Second)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &awsCredentialSource{
		explicit:    explicit,
		client:      client,
		getenv:      os.Getenv,
		now:         time.Now,
		ecsBaseURL:  awsECSCredentialBaseURL,
		imdsBaseURL: awsIMDSBaseURL,
	}
}

func (r *Router) awsCredentialSource(providerID, credentialID, explicit string) *awsCredentialSource {
	key := providerID + "\x00" + credentialID
	r.awsCredentials.mu.Lock()
	defer r.awsCredentials.mu.Unlock()
	if current, found := r.awsCredentials.current[key]; found && current.explicit == explicit {
		return current.source
	}
	source := newAWSCredentialSource(explicit)
	r.awsCredentials.current[key] = managedAWSCredentialSource{explicit: explicit, source: source}
	return source
}

func (r *Router) dropAWSCredentialSources(providerID, credentialID string) {
	r.awsCredentials.mu.Lock()
	defer r.awsCredentials.mu.Unlock()
	if credentialID != "" {
		delete(r.awsCredentials.current, providerID+"\x00"+credentialID)
		return
	}
	prefix := providerID + "\x00"
	for key := range r.awsCredentials.current {
		if strings.HasPrefix(key, prefix) {
			delete(r.awsCredentials.current, key)
		}
	}
}

func (s *awsCredentialSource) Credential(ctx context.Context) (awsCredential, error) {
	for {
		now := s.now()
		s.mu.Lock()
		if s.cacheValid && (s.refreshAt.IsZero() || now.Before(s.refreshAt)) {
			credential := s.cached
			s.mu.Unlock()
			return credential, nil
		}
		if s.lastErr != nil && now.Before(s.retryAt) {
			if s.cacheValid && (s.expiresAt.IsZero() || now.Before(s.expiresAt)) {
				credential := s.cached
				s.mu.Unlock()
				return credential, nil
			}
			err := s.lastErr
			s.mu.Unlock()
			return awsCredential{}, err
		}
		if s.refreshing {
			completed := s.refreshCompleted
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return awsCredential{}, ctx.Err()
			case <-completed:
				continue
			}
		}
		s.refreshing = true
		s.refreshCompleted = make(chan struct{})
		completed := s.refreshCompleted
		s.mu.Unlock()

		credential, expiration, err := s.load(ctx)
		s.mu.Lock()
		if err == nil {
			s.cached = credential
			s.cacheValid = true
			s.refreshAt = awsCredentialRefreshAt(now, expiration)
			s.expiresAt = expiration
			s.lastErr = nil
			s.retryAt = time.Time{}
		} else {
			s.lastErr = err
			s.retryAt = now.Add(time.Second)
			if s.cacheValid && (s.expiresAt.IsZero() || now.Before(s.expiresAt)) {
				credential = s.cached
				err = nil
			}
		}
		s.refreshing = false
		close(completed)
		s.mu.Unlock()
		return credential, err
	}
}

func awsCredentialRefreshAt(now, expiration time.Time) time.Time {
	if expiration.IsZero() {
		return time.Time{}
	}
	advance := expiration.Sub(now) / 2
	if advance > 5*time.Minute {
		advance = 5 * time.Minute
	}
	return expiration.Add(-advance)
}

func (s *awsCredentialSource) load(ctx context.Context) (awsCredential, time.Time, error) {
	if s.explicit != "" {
		credential, err := parseAWSCredential(s.explicit)
		return credential, time.Time{}, err
	}
	accessKey := strings.TrimSpace(s.getenv("AWS_ACCESS_KEY_ID"))
	secretKey := s.getenv("AWS_SECRET_ACCESS_KEY")
	sessionToken := s.getenv("AWS_SESSION_TOKEN")
	if accessKey != "" || secretKey != "" || sessionToken != "" {
		credential := awsCredential{AccessKeyID: accessKey, SecretAccessKey: secretKey, SessionToken: sessionToken}
		if err := validateAWSCredential(credential); err != nil {
			return awsCredential{}, time.Time{}, err
		}
		return credential, time.Time{}, nil
	}
	if relativeURI := strings.TrimSpace(s.getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI")); relativeURI != "" {
		endpoint, err := awsContainerRelativeURL(s.ecsBaseURL, relativeURI)
		if err != nil {
			return awsCredential{}, time.Time{}, err
		}
		return s.loadContainer(ctx, endpoint)
	}
	if fullURI := strings.TrimSpace(s.getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI")); fullURI != "" {
		endpoint, err := awsContainerFullURL(fullURI)
		if err != nil {
			return awsCredential{}, time.Time{}, err
		}
		return s.loadContainer(ctx, endpoint)
	}
	if strings.EqualFold(strings.TrimSpace(s.getenv("AWS_EC2_METADATA_DISABLED")), "true") {
		return awsCredential{}, time.Time{}, errors.New("AWS credential source is unavailable")
	}
	return s.loadIMDS(ctx)
}

func validateAWSCredential(credential awsCredential) error {
	if strings.TrimSpace(credential.AccessKeyID) == "" || len(credential.AccessKeyID) > 128 || credential.SecretAccessKey == "" || len(credential.SecretAccessKey) > 256 || len(credential.SessionToken) > 4096 {
		return errors.New("invalid AWS credential")
	}
	return nil
}

func awsContainerRelativeURL(baseURL, relativeURI string) (string, error) {
	parsed, err := url.ParseRequestURI(relativeURI)
	if err != nil || !strings.HasPrefix(relativeURI, "/") || strings.HasPrefix(relativeURI, "//") || parsed.IsAbs() || parsed.Host != "" || parsed.Fragment != "" {
		return "", errors.New("invalid AWS container credential URI")
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == ".." {
			return "", errors.New("invalid AWS container credential URI")
		}
	}
	return strings.TrimRight(baseURL, "/") + relativeURI, nil
}

func awsContainerFullURL(value string) (string, error) {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Fragment != "" {
		return "", errors.New("invalid AWS container credential URI")
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "localhost", "127.0.0.1", "::1", "169.254.170.2", "169.254.170.23":
	default:
		return "", errors.New("invalid AWS container credential URI")
	}
	return parsed.String(), nil
}

func (s *awsCredentialSource) containerAuthorization() (string, error) {
	if tokenFile := strings.TrimSpace(s.getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE")); tokenFile != "" {
		if !filepath.IsAbs(tokenFile) {
			return "", errors.New("invalid AWS container authorization token file")
		}
		data, err := readAWSAuthorizationTokenFile(tokenFile)
		if err != nil || len(data) == 0 || len(data) > 8<<10 {
			return "", errors.New("invalid AWS container authorization token file")
		}
		return validAuthorizationToken(string(data))
	}
	return validAuthorizationToken(s.getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN"))
}

func readAWSAuthorizationTokenFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (8<<10)+1))
	if err != nil || len(data) > 8<<10 {
		return nil, errors.New("AWS container authorization token file is too large")
	}
	return data, nil
}

func validAuthorizationToken(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) > 8<<10 || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("invalid AWS container authorization token")
	}
	return value, nil
}

func (s *awsCredentialSource) loadContainer(ctx context.Context, endpoint string) (awsCredential, time.Time, error) {
	token, err := s.containerAuthorization()
	if err != nil {
		return awsCredential{}, time.Time{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return awsCredential{}, time.Time{}, errors.New("invalid AWS container credential request")
	}
	if token != "" {
		request.Header.Set("Authorization", token)
	}
	return s.fetchRoleCredential(request)
}

func (s *awsCredentialSource) loadIMDS(ctx context.Context) (awsCredential, time.Time, error) {
	tokenRequest, err := http.NewRequestWithContext(ctx, http.MethodPut, strings.TrimRight(s.imdsBaseURL, "/")+"/latest/api/token", nil)
	if err != nil {
		return awsCredential{}, time.Time{}, errors.New("invalid AWS metadata request")
	}
	tokenRequest.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "21600")
	token, err := s.fetchText(tokenRequest, 4<<10)
	if err != nil || token == "" {
		return awsCredential{}, time.Time{}, errors.New("AWS metadata token is unavailable")
	}
	roleRequest, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.imdsBaseURL, "/")+"/latest/meta-data/iam/security-credentials/", nil)
	roleRequest.Header.Set("X-aws-ec2-metadata-token", token)
	role, err := s.fetchText(roleRequest, 4<<10)
	if err != nil || role == "" || len(role) > 256 || strings.ContainsAny(role, "/?#\r\n") {
		return awsCredential{}, time.Time{}, errors.New("AWS metadata role is unavailable")
	}
	credentialRequest, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.imdsBaseURL, "/")+"/latest/meta-data/iam/security-credentials/"+url.PathEscape(role), nil)
	credentialRequest.Header.Set("X-aws-ec2-metadata-token", token)
	return s.fetchRoleCredential(credentialRequest)
}

func (s *awsCredentialSource) fetchText(request *http.Request, limit int64) (string, error) {
	response, err := s.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("AWS credential endpoint returned status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return "", errors.New("AWS credential endpoint response is invalid")
	}
	return strings.TrimSpace(string(data)), nil
}

func (s *awsCredentialSource) fetchRoleCredential(request *http.Request) (awsCredential, time.Time, error) {
	response, err := s.client.Do(request)
	if err != nil {
		return awsCredential{}, time.Time{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return awsCredential{}, time.Time{}, fmt.Errorf("AWS credential endpoint returned status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, awsCredentialMaxBytes+1))
	if err != nil || len(data) > awsCredentialMaxBytes {
		return awsCredential{}, time.Time{}, errors.New("AWS credential endpoint response is invalid")
	}
	var value awsRoleCredential
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(&value); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return awsCredential{}, time.Time{}, errors.New("AWS credential endpoint response is invalid")
	}
	credential := awsCredential{AccessKeyID: value.AccessKeyID, SecretAccessKey: value.SecretAccessKey, SessionToken: value.Token}
	if err := validateAWSCredential(credential); err != nil || value.Expiration.IsZero() || !value.Expiration.After(s.now()) {
		return awsCredential{}, time.Time{}, errors.New("AWS credential endpoint response is invalid")
	}
	return credential, value.Expiration, nil
}
