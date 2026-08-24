package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

type FailureClass string

const (
	FailureUnknown        FailureClass = "unknown"
	FailureAuthentication FailureClass = "authentication"
	FailureRateLimit      FailureClass = "rate_limit"
	FailureTimeout        FailureClass = "timeout"
	FailureUnavailable    FailureClass = "unavailable"
	FailureContentPolicy  FailureClass = "content_policy"
	FailureClientRequest  FailureClass = "client_request"
	FailurePostProcessing FailureClass = "post_processing"
)

type Error struct {
	Class      FailureClass
	Provider   string
	StatusCode int
	Err        error
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

func failureClass(err error) FailureClass {
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

func tryNextEndpoint(err error) bool {
	switch failureClass(err) {
	case FailureClientRequest, FailureContentPolicy, FailurePostProcessing:
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
