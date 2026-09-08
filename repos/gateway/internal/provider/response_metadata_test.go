package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestResponsesMetadataForwarding(t *testing.T) {
	for _, adapter := range []string{"compatible", "ollama"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", adapter, stream), func(t *testing.T) {
				called := false
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					called = true
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if !reflect.DeepEqual(body["metadata"], map[string]any{"ticket": "42"}) {
						t.Errorf("metadata=%v", body["metadata"])
					}
					if stream {
						_, _ = fmt.Fprint(w, responseTestTerminal)
					} else {
						_, _ = fmt.Fprint(w, `{"id":"r","status":"completed"}`)
					}
				}))
				defer server.Close()
				var request openai.ResponseRequest
				if err := json.Unmarshal([]byte(`{"model":"m","input":"hello","metadata":{"ticket":"42"}}`), &request); err != nil {
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

func TestResponsesMetadataRejectsUnsupportedAdapters(t *testing.T) {
	var request openai.ResponseRequest
	if err := json.Unmarshal([]byte(`{"model":"m","input":"hello","metadata":{"ticket":"42"}}`), &request); err != nil {
		t.Fatal(err)
	}
	for name, client := range map[string]Client{"anthropic": NewAnthropic("http://127.0.0.1:1", "", true), "demo": Demo{}} {
		err := validateResponseAdapter(client, request)
		if err == nil {
			t.Fatalf("%s discarded metadata", name)
		}
	}
}

func TestResponsesMetadataSnapshotReplacesKeys(t *testing.T) {
	wire := "data: {\"type\":\"response.created\",\"response\":{\"metadata\":{\"old\":\"value\"}}}\n\n"
	wire += "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"metadata\":{\"ticket\":\"42\"}}}\n\n"
	result, err := streamResponseData(strings.NewReader(wire), "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Metadata, map[string]string{"ticket": "42"}) {
		t.Fatalf("metadata=%v", result.Metadata)
	}
}
