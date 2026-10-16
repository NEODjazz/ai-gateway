package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestResponsesOutputLimitAliasForwarding(t *testing.T) {
	for _, value := range []string{`1`, `20`, `null`} {
		var want any
		if err := json.Unmarshal([]byte(value), &want); err != nil {
			t.Fatal(err)
		}
		for _, adapter := range []string{"compatible", "ollama"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%v/%s", adapter, stream, value), func(t *testing.T) {
					called := false
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						called = true
						var body map[string]any
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						if _, exists := body["max_tokens"]; exists {
							t.Error("legacy max_tokens forwarded")
						}
						if !reflect.DeepEqual(body["max_output_tokens"], want) {
							t.Errorf("max_tokens=%v", body["max_output_tokens"])
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
					if err := json.Unmarshal([]byte(`{"model":"m","input":"hello","max_tokens":`+value+`}`), &request); err != nil {
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
