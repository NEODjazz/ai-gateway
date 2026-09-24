package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestOllamaResponsesToolChoiceControlsForwardedTools(t *testing.T) {
	for _, test := range []struct {
		choice    string
		wantTools bool
	}{
		{choice: "none", wantTools: false},
		{choice: "auto", wantTools: true},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", test.choice, stream), func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					var body map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					_, hasTools := body["tools"]
					if hasTools != test.wantTools {
						t.Errorf("upstream tools present=%t, want %t", hasTools, test.wantTools)
					}
					if _, supplied := body["tool_choice"]; supplied {
						t.Errorf("unsupported tool_choice reached Ollama: %s", body["tool_choice"])
					}
					if stream {
						_, _ = fmt.Fprint(w, ollamaResponseTestTerminal)
					} else {
						_, _ = fmt.Fprint(w, ollamaResponseTestJSON)
					}
				}))
				t.Cleanup(server.Close)
				request := openai.ResponseRequest{Model: "m", Input: "hello", ToolChoice: test.choice, Tools: []openai.ResponseTool{{Type: "function", Name: "lookup", Parameters: map[string]any{"type": "object"}}}}
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
				if len(request.Tools) != 1 || request.ToolChoice != test.choice {
					t.Fatalf("request was mutated: %+v", request)
				}
			})
		}
	}
}
