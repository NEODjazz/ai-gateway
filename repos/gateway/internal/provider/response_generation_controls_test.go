package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestResponsesGenerationControlsForwarding(t *testing.T) {
	frequency, presence, maxToolCalls := 0.5, -0.25, 7
	for _, adapter := range []string{"compatible", "openrouter"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", adapter, stream), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if body["frequency_penalty"] != frequency || body["presence_penalty"] != presence || body["max_tool_calls"] != float64(maxToolCalls) {
						t.Fatalf("generation controls lost: %#v", body)
					}
					if stream {
						_, _ = fmt.Fprint(w, responseTestTerminal)
					} else {
						_, _ = fmt.Fprint(w, `{"id":"r","status":"completed"}`)
					}
				}))
				defer server.Close()
				request := openai.ResponseRequest{Model: "m", Input: "hello", FrequencyPenalty: &frequency, PresencePenalty: &presence, MaxToolCalls: &maxToolCalls}
				var client Client
				if adapter == "openrouter" {
					value := NewOpenRouter(server.URL, "", true, "")
					client = value
				} else {
					value := NewOpenAICompatible(server.URL, "", true)
					client = value
				}
				var err error
				if stream {
					_, err = client.(StreamingResponseClient).StreamResponses(t.Context(), request, func(string, string) error { return nil })
				} else {
					_, err = client.Responses(t.Context(), request)
				}
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestResponsesGenerationControlsRejectUnsupportedAdapters(t *testing.T) {
	frequency, presence, maxToolCalls := 0.5, -0.25, 7
	obfuscation := false
	tests := []struct {
		field   string
		request openai.ResponseRequest
	}{
		{"frequency_penalty", openai.ResponseRequest{FrequencyPenalty: &frequency}},
		{"presence_penalty", openai.ResponseRequest{PresencePenalty: &presence}},
		{"max_tool_calls", openai.ResponseRequest{MaxToolCalls: &maxToolCalls}},
		{"stream_options", openai.ResponseRequest{Stream: true, StreamOptions: &openai.ResponseStreamOptions{IncludeObfuscation: &obfuscation}}},
	}
	clients := map[string]Client{
		"anthropic": NewAnthropic("http://127.0.0.1:1", "", true),
		"ollama":    NewOllama("http://127.0.0.1:1", true),
		"deepseek":  NewDeepSeek("http://127.0.0.1:1", "", true),
		"demo":      Demo{},
	}
	for name, client := range clients {
		for _, test := range tests {
			err := validateResponseAdapter(client, test.request)
			var failure *Error
			if !errors.As(err, &failure) || failure.Param != test.field || failure.StatusCode != http.StatusBadRequest || failure.UpstreamCode != "unsupported_parameter" {
				t.Fatalf("%s discarded %s: %v", name, test.field, err)
			}
		}
	}
}

func TestOpenRouterResponsesForwardsServiceTier(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body["service_tier"] != "flex" {
					t.Fatalf("service_tier was not forwarded: %#v", body)
				}
				if streaming {
					_, _ = fmt.Fprint(w, responseTestTerminal)
				} else {
					_, _ = fmt.Fprint(w, `{"id":"r","object":"response","status":"completed","model":"m","output":[]}`)
				}
			}))
			defer server.Close()

			client := NewOpenRouter(server.URL, "", true, "")
			request := openai.ResponseRequest{Model: "m", Input: "hello", ServiceTier: "flex", Stream: streaming}
			var err error
			if streaming {
				_, err = client.StreamResponses(t.Context(), request, func(string, string) error { return nil })
			} else {
				_, err = client.Responses(t.Context(), request)
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
