package provider

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestAnthropicResponsePhasePolicy(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	client := NewAnthropic(server.URL, "", true)
	for _, phase := range []string{"commentary", "final_answer", ""} {
		request := openai.ResponseRequest{Model: "m", Input: []any{map[string]any{"type": "message", "role": "assistant", "phase": phase, "content": "hello"}}}
		for _, stream := range []bool{false, true} {
			var err error
			if stream {
				_, err = client.StreamResponses(t.Context(), request, func(string, string) error { return nil })
			} else {
				_, err = client.Responses(t.Context(), request)
			}
			var failure *Error
			if !errors.As(err, &failure) || failure.StatusCode != 400 || failure.Param != "input.phase" || failure.UpstreamCode != "unsupported_parameter" {
				t.Errorf("phase=%q stream=%v err=%v", phase, stream, err)
			}
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("requests reached upstream: %d", calls.Load())
	}
	for _, item := range []map[string]any{{"role": "assistant", "content": "hello"}, {"role": "assistant", "content": "hello", "phase": nil}} {
		if err := client.ValidateResponseParameters(openai.ResponseRequest{Input: []any{item}}); err != nil {
			t.Fatal(err)
		}
	}
}
