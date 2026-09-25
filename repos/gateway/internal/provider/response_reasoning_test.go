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

func TestResponsesReasoningForwarding(t *testing.T) {
	for _, adapter := range []string{"compatible", "ollama"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", adapter, stream), func(t *testing.T) {
				requestBody := `{"model":"m","input":"hello","reasoning":{"effort":"high","summary":"auto","generate_summary":"auto","context":"auto","mode":"standard"}}`
				wantReasoning := map[string]any{"effort": "high", "summary": "auto", "generate_summary": "auto", "context": "auto", "mode": "standard"}
				if adapter == "ollama" {
					requestBody = `{"model":"m","input":"hello","reasoning":{"effort":"high"}}`
					wantReasoning = map[string]any{"effort": "high"}
				}
				called := false
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					called = true
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if !reflect.DeepEqual(body["reasoning"], wantReasoning) {
						t.Errorf("reasoning=%v", body["reasoning"])
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
				if err := json.Unmarshal([]byte(requestBody), &request); err != nil {
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

func TestResponsesReasoningRejectsUnsupportedAdapters(t *testing.T) {
	var request openai.ResponseRequest
	if err := json.Unmarshal([]byte(`{"model":"m","input":"hello","reasoning":{"effort":"high","summary":"auto","generate_summary":"auto","context":"auto","mode":"standard"}}`), &request); err != nil {
		t.Fatal(err)
	}
	for name, client := range map[string]Client{"anthropic": NewAnthropic("http://127.0.0.1:1", "", true), "demo": Demo{}} {
		err := validateResponseAdapter(client, request)
		if err == nil {
			t.Fatalf("%s discarded reasoning", name)
		}
	}
}

func TestOllamaResponsesDefaultReasoningUsesModelDefault(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				reasoning, _ := body["reasoning"].(map[string]any)
				if _, supplied := reasoning["effort"]; supplied {
					t.Errorf("model default was overridden: %v", reasoning)
				}
				if stream {
					_, _ = fmt.Fprint(w, ollamaResponseTestTerminal)
				} else {
					_, _ = fmt.Fprint(w, ollamaResponseTestJSON)
				}
			}))
			t.Cleanup(server.Close)
			effort := "default"
			request := openai.ResponseRequest{Model: "m", Input: "hello", Reasoning: &openai.ResponseReasoning{Effort: &effort}}
			client := NewOllama(server.URL, true)
			var err error
			if stream {
				_, err = client.StreamResponses(t.Context(), request, nil)
			} else {
				_, err = client.Responses(t.Context(), request)
			}
			if err != nil || effort != "default" || request.Reasoning.Effort != &effort {
				t.Fatalf("err=%v original effort=%q", err, effort)
			}
		})
	}
}
