package provider

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestAnthropicRejectsOpaqueResponseContext(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	for _, item := range []map[string]any{
		{"type": "reasoning", "summary": []any{}},
		{"type": "reasoning", "encrypted_content": "opaque"},
		{"type": "compaction", "encrypted_content": "opaque"},
	} {
		request := openai.ResponseRequest{Model: "m", Input: []any{item, map[string]any{"role": "user", "content": "hello"}}}
		client := NewAnthropic(server.URL, "", true)
		for _, stream := range []bool{false, true} {
			var err error
			if stream {
				_, err = client.StreamResponses(t.Context(), request, func(string, string) error { return nil })
			} else {
				_, err = client.Responses(t.Context(), request)
			}
			var failure *Error
			if !errors.As(err, &failure) || failure.StatusCode != 400 || failure.Param != "input" || failure.UpstreamCode != "unsupported_parameter" {
				t.Errorf("type=%s stream=%v err=%v", item["type"], stream, err)
			}
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid requests reached upstream: %d", calls.Load())
	}
}

func TestResponseReasoningPolicyKeepsMessageInputs(t *testing.T) {
	for _, input := range []any{"hello", []any{map[string]any{"role": "user", "content": "hello"}}, []any{map[string]any{"role": "assistant", "content": "reasoning"}}} {
		if err := (Anthropic{}).ValidateResponseParameters(openai.ResponseRequest{Input: input}); err != nil {
			t.Fatal(err)
		}
	}
}
