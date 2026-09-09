package provider

import (
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"errors"
	"strings"
	"testing"
)

func TestRouterRejectsInvalidResponseOptionsBeforeRouting(t *testing.T) {
	badCount := 21
	badTruncation := "unknown"
	for _, request := range []openai.ResponseRequest{{TopLogprobs: &badCount}, {Truncation: &badTruncation}, {SafetyIdentifier: strings.Repeat("я", 65)}, {ServiceTier: "unknown"}, {Text: map[string]any{"verbosity": "unknown"}}} {
		ctx := modules.RequestContext{ResponseRequest: &request}
		router := New(Config{})
		_, err := router.Responses(t.Context(), ctx)
		var failure *Error
		if !errors.As(err, &failure) || failure.StatusCode != 400 || failure.UpstreamCode != "invalid_request" || failure.Class != FailureClientRequest {
			t.Fatalf("JSON: %v", err)
		}
		called := false
		_, handled, err := router.StreamResponses(t.Context(), ctx, func(string, string) error { called = true; return nil })
		if !handled || called || !errors.As(err, &failure) || failure.StatusCode != 400 || failure.UpstreamCode != "invalid_request" {
			t.Fatalf("SSE: handled=%v called=%v err=%v", handled, called, err)
		}
	}
}
