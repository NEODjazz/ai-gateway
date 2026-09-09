package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type FailureClass string

const (
	FailureUnknown        FailureClass = "unknown"
	FailureAuthentication FailureClass = "authentication"
	FailureRateLimit      FailureClass = "rate_limit"
	FailureTimeout        FailureClass = "timeout"
	FailureUnavailable    FailureClass = "unavailable"
	FailureContextLength  FailureClass = "context_length"
	FailureContentPolicy  FailureClass = "content_policy"
	FailureClientRequest  FailureClass = "client_request"
	FailurePostProcessing FailureClass = "post_processing"
)

type Error struct {
	Class        FailureClass
	Provider     string
	StatusCode   int
	UpstreamCode string
	Param        string
	RetryAfter   time.Duration
	Err          error
}

func (e *Error) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("%s provider returned status %d: %v", e.Provider, e.StatusCode, e.Err)
	}
	return fmt.Sprintf("%s provider failed: %v", e.Provider, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

func statusError(provider string, statusCode int) error {
	class := FailureUnknown
	switch {
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		class = FailureAuthentication
	case statusCode == http.StatusTooManyRequests:
		class = FailureRateLimit
	case statusCode == http.StatusRequestTimeout || statusCode == http.StatusGatewayTimeout:
		class = FailureTimeout
	case statusCode == http.StatusUnavailableForLegalReasons:
		class = FailureContentPolicy
	case statusCode >= 400 && statusCode < 500:
		class = FailureClientRequest
	case statusCode >= 500:
		class = FailureUnavailable
	}
	return &Error{Class: class, Provider: provider, StatusCode: statusCode, Err: fmt.Errorf("HTTP %s", http.StatusText(statusCode))}
}

var safeUpstreamIdentifier = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// responseStatusError preserves only bounded, identifier-like diagnostics.
// Raw upstream messages and response bodies can echo request content and must
// never be returned to gateway clients or retained in metadata.
func responseStatusError(provider string, response *http.Response) error {
	err := statusError(provider, response.StatusCode)
	var providerErr *Error
	if !errors.As(err, &providerErr) {
		return err
	}
	providerErr.RetryAfter = retryAfterFromHeaders(response.Header, time.Now())
	if response.Body == nil {
		return providerErr
	}
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if readErr != nil || len(payload) > 64<<10 {
		return err
	}
	var body struct {
		Error struct {
			Code   json.RawMessage `json:"code"`
			Status string          `json:"status"`
			Type   string          `json:"type"`
			Param  string          `json:"param"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &body) != nil {
		return err
	}
	var code string
	if len(body.Error.Code) > 0 {
		if decodeErr := json.Unmarshal(body.Error.Code, &code); decodeErr != nil {
			var numeric json.Number
			if json.Unmarshal(body.Error.Code, &numeric) != nil {
				return err
			}
		}
	}
	code = strings.TrimSpace(code)
	if code == "" {
		code = strings.TrimSpace(body.Error.Type)
	}
	if code == "" {
		code = strings.TrimSpace(body.Error.Status)
	}
	if safeUpstreamIdentifier.MatchString(code) {
		providerErr.UpstreamCode = code
		switch strings.ToLower(providerErr.UpstreamCode) {
		case "context_length_exceeded", "context_window_exceeded":
			providerErr.Class = FailureContextLength
		case "content_policy_violation", "content_filter":
			providerErr.Class = FailureContentPolicy
		}
	}
	if safeUpstreamIdentifier.MatchString(strings.TrimSpace(body.Error.Param)) {
		providerErr.Param = strings.TrimSpace(body.Error.Param)
	}
	return providerErr
}

func retryAfterFromHeaders(headers http.Header, now time.Time) time.Duration {
	if milliseconds := strings.TrimSpace(headers.Get("Retry-After-Ms")); milliseconds != "" {
		if delay, ok := parseRetryAfterDuration(milliseconds, time.Millisecond); ok {
			return delay
		}
	}
	value := strings.TrimSpace(headers.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if delay, ok := parseRetryAfterDuration(value, time.Second); ok {
		return delay
	}
	if retryAt, err := http.ParseTime(value); err == nil && retryAt.After(now) {
		return retryAt.Sub(now)
	}
	return 0
}

// Validate in floating-point space before conversion: overflow, NaN and infinity
// must not become a negative duration or suppress a valid fallback header.
func parseRetryAfterDuration(raw string, unit time.Duration) (time.Duration, bool) {
	value, err := strconv.ParseFloat(raw, 64)
	scaled := value * float64(unit)
	if err != nil || !(scaled > 0 && scaled < float64(int64(1<<63-1))) {
		return 0, false
	}
	return time.Duration(scaled), true
}

func providerRetryAfter(err error) time.Duration {
	var quotaErr *DeploymentQuotaError
	if errors.As(err, &quotaErr) {
		return quotaErr.RetryAfter
	}
	var providerErr *Error
	if errors.As(err, &providerErr) {
		return providerErr.RetryAfter
	}
	return 0
}

func failureClass(err error) FailureClass {
	var quotaErr *DeploymentQuotaError
	if errors.As(err, &quotaErr) {
		return FailureRateLimit
	}
	var admissionErr *AdmissionError
	if errors.As(err, &admissionErr) {
		return FailureRateLimit
	}
	var providerErr *Error
	if errors.As(err, &providerErr) {
		return providerErr.Class
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return FailureTimeout
	}
	return FailureUnknown
}

func retrySameEndpoint(err error) bool {
	switch failureClass(err) {
	case FailureTimeout, FailureUnavailable:
		return true
	default:
		return false
	}
}

func retrySameEndpointWithPolicy(endpoint Endpoint, err error) bool {
	class := failureClass(err)
	if retries, configured := endpoint.RetryPolicy[string(class)]; configured {
		switch class {
		case FailureTimeout, FailureUnavailable, FailureRateLimit, FailureUnknown:
			return retries > 0
		default:
			return false
		}
	}
	return retrySameEndpoint(err)
}

func endpointRetryLimit(endpoint Endpoint, err error) int {
	if retries, configured := endpoint.RetryPolicy[string(failureClass(err))]; configured {
		return retries
	}
	return endpoint.MaxRetries
}

func endpointMaxRetries(endpoint Endpoint) int {
	maximum := endpoint.MaxRetries
	for _, retries := range endpoint.RetryPolicy {
		if retries > maximum {
			maximum = retries
		}
	}
	return maximum
}

func tryNextEndpoint(err error) bool {
	switch failureClass(err) {
	case FailureClientRequest, FailureContextLength, FailureContentPolicy, FailurePostProcessing:
		return false
	default:
		return true
	}
}

func shouldCooldown(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	switch failureClass(err) {
	case FailureAuthentication, FailureRateLimit, FailureTimeout, FailureUnavailable, FailureUnknown:
		return true
	default:
		return false
	}
}
