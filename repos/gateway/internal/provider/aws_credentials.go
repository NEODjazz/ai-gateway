package provider

import (
	"context"
	"encoding/json"
	"encoding/xml"
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
	awsWebIdentityMaxBytes  = 64 << 10
)

type awsCredentialSource struct {
	explicit         string
	region           string
	client           *http.Client
	getenv           func(string) string
	now              func() time.Time
	ecsBaseURL       string
	imdsBaseURL      string
	stsBaseURL       string
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
	region   string
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

type awsWebIdentityResponse struct {
	Result struct {
		Credentials struct {
			AccessKeyID     string    `xml:"AccessKeyId"`
			SecretAccessKey string    `xml:"SecretAccessKey"`
			SessionToken    string    `xml:"SessionToken"`
			Expiration      time.Time `xml:"Expiration"`
		} `xml:"Credentials"`
	} `xml:"AssumeRoleWithWebIdentityResult"`
}

func newAWSCredentialSource(explicit, region string) *awsCredentialSource {
	client := newProviderHTTPClient(2 * time.Second)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &awsCredentialSource{
		explicit:    explicit,
		region:      region,
		client:      client,
		getenv:      os.Getenv,
		now:         time.Now,
		ecsBaseURL:  awsECSCredentialBaseURL,
		imdsBaseURL: awsIMDSBaseURL,
		stsBaseURL:  awsSTSEndpoint(region),
	}
}

func (r *Router) awsCredentialSource(providerID, credentialID, explicit, region string) *awsCredentialSource {
	key := providerID + "\x00" + credentialID
	r.awsCredentials.mu.Lock()
	defer r.awsCredentials.mu.Unlock()
	if current, found := r.awsCredentials.current[key]; found && current.explicit == explicit && current.region == region {
		return current.source
	}
	source := newAWSCredentialSource(explicit, region)
	r.awsCredentials.current[key] = managedAWSCredentialSource{explicit: explicit, region: region, source: source}
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
	roleARN := strings.TrimSpace(s.getenv("AWS_ROLE_ARN"))
	tokenFile := strings.TrimSpace(s.getenv("AWS_WEB_IDENTITY_TOKEN_FILE"))
	if roleARN != "" || tokenFile != "" {
		if roleARN == "" || tokenFile == "" {
			return awsCredential{}, time.Time{}, errors.New("incomplete AWS web identity configuration")
		}
		return s.loadWebIdentity(ctx, roleARN, tokenFile, strings.TrimSpace(s.getenv("AWS_ROLE_SESSION_NAME")))
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

func awsSTSEndpoint(region string) string {
	suffix := "amazonaws.com"
	if strings.HasPrefix(region, "cn-") {
		suffix = "amazonaws.com.cn"
	}
	return "https://sts." + region + "." + suffix
}

func (s *awsCredentialSource) loadWebIdentity(ctx context.Context, roleARN, tokenFile, sessionName string) (awsCredential, time.Time, error) {
	if !validAWSRoleARN(roleARN) || !filepath.IsAbs(tokenFile) {
		return awsCredential{}, time.Time{}, errors.New("invalid AWS web identity configuration")
	}
	if sessionName == "" {
		sessionName = "ai-gateway"
	}
	if !validAWSRoleSessionName(sessionName) {
		return awsCredential{}, time.Time{}, errors.New("invalid AWS web identity configuration")
	}
	token, err := readAWSBoundedFile(tokenFile, awsWebIdentityMaxBytes)
	if err != nil {
		return awsCredential{}, time.Time{}, errors.New("invalid AWS web identity token file")
	}
	token = []byte(strings.TrimSpace(string(token)))
	if len(token) == 0 || strings.ContainsAny(string(token), "\r\n") {
		return awsCredential{}, time.Time{}, errors.New("invalid AWS web identity token file")
	}
	form := url.Values{
		"Action":           {"AssumeRoleWithWebIdentity"},
		"Version":          {"2011-06-15"},
		"RoleArn":          {roleARN},
		"RoleSessionName":  {sessionName},
		"WebIdentityToken": {string(token)},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.stsBaseURL, strings.NewReader(form.Encode()))
	if err != nil {
		return awsCredential{}, time.Time{}, errors.New("invalid AWS web identity request")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.client.Do(request)
	if err != nil {
		return awsCredential{}, time.Time{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return awsCredential{}, time.Time{}, fmt.Errorf("AWS web identity endpoint returned status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, awsCredentialMaxBytes+1))
	if err != nil || len(data) > awsCredentialMaxBytes {
		return awsCredential{}, time.Time{}, errors.New("AWS web identity response is invalid")
	}
	var value awsWebIdentityResponse
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(&value); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return awsCredential{}, time.Time{}, errors.New("AWS web identity response is invalid")
	}
	credential := awsCredential{AccessKeyID: value.Result.Credentials.AccessKeyID, SecretAccessKey: value.Result.Credentials.SecretAccessKey, SessionToken: value.Result.Credentials.SessionToken}
	if err := validateAWSCredential(credential); err != nil || value.Result.Credentials.Expiration.IsZero() || !value.Result.Credentials.Expiration.After(s.now()) {
		return awsCredential{}, time.Time{}, errors.New("AWS web identity response is invalid")
	}
	return credential, value.Result.Credentials.Expiration, nil
}

func validAWSRoleARN(value string) bool {
	if len(value) < 20 || len(value) > 2048 || strings.ContainsAny(value, "\x00\r\n\t ") {
		return false
	}
	parts := strings.SplitN(value, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[1] == "" || parts[2] != "iam" || parts[3] != "" || len(parts[4]) != 12 || !strings.HasPrefix(parts[5], "role/") || len(parts[5]) == len("role/") {
		return false
	}
	for _, char := range parts[4] {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func validAWSRoleSessionName(value string) bool {
	if len(value) < 2 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && !strings.ContainsRune("+=,.@_-", char) {
			return false
		}
	}
	return true
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
	return readAWSBoundedFile(path, 8<<10)
}

func readAWSBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("AWS credential token path is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("AWS credential token file is too large")
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
