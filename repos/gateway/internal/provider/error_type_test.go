package provider

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestProviderErrorTypeDiagnostic(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"error":{"type":"rate_limit_error","message":"private prompt"}}`, "rate_limit_error"},
		{`{"error":{"type":"overloaded_error","code":"specific_code"}}`, "specific_code"},
		{`{"error":{"type":"private prompt"}}`, ""},
		{`{"error":{"type":"` + strings.Repeat("a", 129) + `"}}`, ""},
		{`{"error":{"type":" rate_limit_error "}}`, "rate_limit_error"},
	} {
		response := &http.Response{StatusCode: 429, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}
		err := responseStatusError("anthropic", response)
		var failure *Error
		if !errors.As(err, &failure) || failure.UpstreamCode != tc.want || failure.Class != FailureRateLimit {
			t.Fatalf("wanted=%q err=%+v", tc.want, failure)
		}
		if strings.Contains(err.Error(), "private prompt") {
			t.Fatal("upstream message leaked")
		}
	}
}

func TestProviderNumericErrorCodeWithStatus(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"private prompt"}}`, "RESOURCE_EXHAUSTED"},
		{`{"error":{"code":400,"status":"INVALID_ARGUMENT","param":"input"}}`, "INVALID_ARGUMENT"},
		{`{"error":{"code":"specific","status":"INVALID_ARGUMENT"}}`, "specific"},
		{`{"error":{"code":429,"type":"rate_limit_error","status":"RESOURCE_EXHAUSTED"}}`, "rate_limit_error"},
		{`{"error":{"code":429,"status":"private prompt"}}`, ""},
	} {
		response := &http.Response{StatusCode: 429, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}
		err := responseStatusError("gemini", response)
		var failure *Error
		if !errors.As(err, &failure) || failure.UpstreamCode != tc.want || failure.Class != FailureRateLimit {
			t.Fatalf("want=%q error=%+v", tc.want, failure)
		}
		if strings.Contains(err.Error(), "private prompt") {
			t.Fatal("upstream message leaked")
		}
	}
}
