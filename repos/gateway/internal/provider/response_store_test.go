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

func TestResponsesStoreForwarding(t *testing.T) {
	for _, adapter := range []string{"compatible", "ollama"} {
		for _, stream := range []bool{false, true} {
			for _, value := range []string{"false", "true", "null"} {
				if adapter == "ollama" && value == "true" {
					continue
				}
				t.Run(fmt.Sprintf("%s/%v/%s", adapter, stream, value), func(t *testing.T) {
					called := false
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						called = true
						var body map[string]any
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						var want any
						if value != "null" {
							want = value == "true"
						}
						if body["store"] != want {
							t.Errorf("store=%v want=%v", body["store"], want)
						}
						if stream {
							_, _ = fmt.Fprint(w, responseTestTerminal)
						} else {
							_, _ = fmt.Fprint(w, `{"id":"r","status":"completed"}`)
						}
					}))
					defer server.Close()
					var request openai.ResponseRequest
					if err := json.Unmarshal([]byte(`{"model":"m","input":"hello","store":`+value+`}`), &request); err != nil {
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

func TestResponsesStoreRejectsUnsupportedAdapters(t *testing.T) {
	var request openai.ResponseRequest
	if err := json.Unmarshal([]byte(`{"model":"m","input":"hello","store":false}`), &request); err != nil {
		t.Fatal(err)
	}
	for name, client := range map[string]Client{"anthropic": NewAnthropic("http://127.0.0.1:1", "", true), "demo": Demo{}} {
		err := validateResponseAdapter(client, request)
		var failure *Error
		if !errors.As(err, &failure) || failure.Param != "store" || failure.StatusCode != 400 {
			t.Fatalf("%s err=%v", name, err)
		}
	}
}

func TestOllamaResponsesRejectsStoredOutput(t *testing.T) {
	value := true
	request := openai.ResponseRequest{Model: "m", Input: "hello", Store: &value}
	assertUnsupportedParameter(t, (Ollama{}).ValidateResponseParameters(request), "store")
}
