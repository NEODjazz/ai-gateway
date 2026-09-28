package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

type ollamaRoundTripFunc func(*http.Request) (*http.Response, error)

func (f ollamaRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestOllamaCloudRejectsStructuredOutputBeforeUpstream(t *testing.T) {
	var calls atomic.Int32
	client := NewOllama("https://ollama.com/api", true)
	client.client.Transport = ollamaRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unexpected upstream request")
	})
	for _, format := range []*openai.ResponseFormat{
		{Type: "json_object"},
		{Type: "json_schema", JSONSchema: &openai.JSONSchemaFormat{Schema: map[string]any{"type": "object"}}},
	} {
		request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ResponseFormat: format}
		_, err := client.ChatCompletions(t.Context(), request)
		assertUnsupportedParameter(t, err, "response_format")
		request.Stream = true
		_, err = client.StreamChatCompletions(t.Context(), request, func(string) error { return nil })
		assertUnsupportedParameter(t, err, "response_format")
	}
	for _, format := range []string{"json_object", "json_schema"} {
		request := openai.ResponseRequest{Model: "model", Input: "hello", Text: map[string]any{"format": map[string]any{"type": format, "schema": map[string]any{"type": "object"}}}}
		if format == "json_object" {
			request.Text = map[string]any{"format": map[string]any{"type": format}}
		}
		_, err := client.Responses(t.Context(), request)
		assertUnsupportedParameter(t, err, "text.format")
		request.Stream = true
		_, err = client.StreamResponses(t.Context(), request, func(string, string) error { return nil })
		assertUnsupportedParameter(t, err, "text.format")
	}
	if calls.Load() != 0 {
		t.Fatalf("cloud structured output reached upstream %d times", calls.Load())
	}
}

func TestOllamaResponsesMapsJSONObjectFormat(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body struct {
					Text struct {
						Format struct {
							Type   string         `json:"type"`
							Schema map[string]any `json:"schema"`
						} `json:"format"`
					} `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.Text.Format.Type != "json_schema" || body.Text.Format.Schema["type"] != "object" {
					t.Errorf("Ollama cannot enforce format: %+v", body.Text.Format)
				}
				if stream {
					_, _ = fmt.Fprint(w, ollamaResponseTestTerminal)
				} else {
					_, _ = fmt.Fprint(w, ollamaResponseTestJSON)
				}
			}))
			t.Cleanup(server.Close)
			format := map[string]any{"type": "json_object"}
			request := openai.ResponseRequest{Model: "m", Input: "hello", Text: map[string]any{"format": format}}
			client := NewOllama(server.URL, true)
			var err error
			if stream {
				_, err = client.StreamResponses(t.Context(), request, nil)
			} else {
				_, err = client.Responses(t.Context(), request)
			}
			if err != nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if format["type"] != "json_object" {
				t.Fatalf("request was mutated: %+v", format)
			}
		})
	}
}

func TestOllamaResponsesRejectsJSONObjectExtraControls(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	request := openai.ResponseRequest{Model: "m", Input: "hello", Text: map[string]any{
		"format": map[string]any{"type": "json_object", "schema": map[string]any{"type": "array"}},
	}}
	client := NewOllama(server.URL, true)
	_, err := client.Responses(t.Context(), request)
	assertUnsupportedParameter(t, err, "text.format")
	_, err = client.StreamResponses(t.Context(), request, nil)
	assertUnsupportedParameter(t, err, "text.format")
	if calls.Load() != 0 {
		t.Fatalf("invalid format reached upstream: %d", calls.Load())
	}
}

func TestOllamaResponsesRejectsIgnoredTextFormats(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	for _, test := range []struct {
		name, code, param string
		format            map[string]any
	}{
		{name: "unknown type", code: "unsupported_parameter", param: "text.format.type", format: map[string]any{"type": "json_lines"}},
		{name: "missing type", code: "invalid_request", param: "text.format.type", format: map[string]any{}},
		{name: "missing schema", code: "invalid_request", param: "text.format.schema", format: map[string]any{"type": "json_schema"}},
		{name: "ignored strict", code: "unsupported_parameter", param: "text.format.strict", format: map[string]any{"type": "json_schema", "schema": map[string]any{"type": "object"}, "strict": true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := openai.ResponseRequest{Model: "m", Input: "hello", Text: map[string]any{"format": test.format}}
			client := NewOllama(server.URL, true)
			for _, stream := range []bool{false, true} {
				var err error
				if stream {
					_, err = client.StreamResponses(t.Context(), request, nil)
				} else {
					_, err = client.Responses(t.Context(), request)
				}
				var failure *Error
				if !errors.As(err, &failure) || failure.UpstreamCode != test.code || failure.Param != test.param {
					t.Fatalf("stream=%v: unexpected error: %v", stream, err)
				}
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("ignored text format reached upstream: %d", calls.Load())
	}
}
