package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestResponsesTopLogprobsForwarding(t *testing.T) {
	for _, value := range []string{`0`, `20`, `null`} {
		var want any
		if err := json.Unmarshal([]byte(value), &want); err != nil {
			t.Fatal(err)
		}
		adapters := []string{"compatible"}
		if value == `null` {
			adapters = append(adapters, "ollama")
		}
		for _, adapter := range adapters {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%v/%s", adapter, stream, value), func(t *testing.T) {
					called := false
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						called = true
						var body map[string]any
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						if !reflect.DeepEqual(body["top_logprobs"], want) {
							t.Errorf("top_logprobs=%v", body["top_logprobs"])
						}
						if stream {
							if adapter == "ollama" {
								_, _ = fmt.Fprint(w, ollamaResponseTestTerminal)
							} else {
								_, _ = fmt.Fprint(w, responseTestTerminal)
							}
						} else {
							if adapter == "ollama" {
								_, _ = fmt.Fprint(w, ollamaResponseTestJSON)
							} else {
								_, _ = fmt.Fprint(w, `{"id":"r","status":"completed"}`)
							}
						}
					}))
					defer server.Close()
					var request openai.ResponseRequest
					if err := json.Unmarshal([]byte(`{"model":"m","input":"hello","top_logprobs":`+value+`}`), &request); err != nil {
						t.Fatal(err)
					}
					var err error
					if adapter == "compatible" {
						p := NewOpenAICompatible(server.URL, "", true)
						if stream {
							_, err = p.StreamResponses(t.Context(), request, func(string, string) error { return nil })
						} else {
							_, err = p.Responses(t.Context(), request)
						}
					} else {
						p := NewOllama(server.URL, true)
						if stream {
							_, err = p.StreamResponses(t.Context(), request, func(string, string) error { return nil })
						} else {
							_, err = p.Responses(t.Context(), request)
						}
					}
					if err != nil || !called {
						t.Fatalf("called=%v err=%v", called, err)
					}
				})
			}
		}
	}

}

func TestResponsesTopLogprobsRejectsUnsupportedAdapters(t *testing.T) {
	var request openai.ResponseRequest
	if err := json.Unmarshal([]byte(`{"model":"m","input":"hello","top_logprobs":0}`), &request); err != nil {
		t.Fatal(err)
	}
	for name, client := range map[string]Client{"anthropic": NewAnthropic("http://127.0.0.1:1", "", true), "demo": Demo{}, "ollama": NewOllama("http://127.0.0.1:1", true)} {
		err := validateResponseAdapter(client, request)
		var failure *Error
		if !errors.As(err, &failure) || failure.Param != "top_logprobs" || failure.StatusCode != 400 || failure.UpstreamCode != "unsupported_parameter" {
			t.Fatalf("%s discarded top_logprobs", name)
		}
	}
}
