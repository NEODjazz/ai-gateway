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

func TestResponsesIncludeForwarding(t *testing.T) {
	for _, adapter := range []string{"compatible"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", adapter, stream), func(t *testing.T) {
				called := false
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					called = true
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if !reflect.DeepEqual(body["include"], []any{"reasoning.encrypted_content"}) {
						t.Errorf("include=%v", body["include"])
					}
					if stream {
						_, _ = fmt.Fprint(w, responseTestTerminal)
					} else {
						_, _ = fmt.Fprint(w, `{"id":"r","status":"completed"}`)
					}
				}))
				defer server.Close()
				var request openai.ResponseRequest
				if err := json.Unmarshal([]byte(`{"model":"m","input":"hello","include":["reasoning.encrypted_content"]}`), &request); err != nil {
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

func TestResponsesIncludeRejectsUnsupportedAdapters(t *testing.T) {
	var request openai.ResponseRequest
	if err := json.Unmarshal([]byte(`{"model":"m","input":"hello","include":["reasoning.encrypted_content"]}`), &request); err != nil {
		t.Fatal(err)
	}
	for name, client := range map[string]Client{"anthropic": NewAnthropic("http://127.0.0.1:1", "", true), "demo": Demo{}, "ollama": NewOllama("http://127.0.0.1:1", true)} {
		err := validateResponseAdapter(client, request)
		if err == nil {
			t.Fatalf("%s discarded include", name)
		}
	}
}
