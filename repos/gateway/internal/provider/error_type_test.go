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
