package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestOllamaChatToolChoiceControlsForwardedTools(t *testing.T) {
	for _, test := range []struct {
		choice    string
		wantTools bool
	}{
		{choice: "auto", wantTools: true},
		{choice: "none", wantTools: false},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", test.choice, stream), func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					var body map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						return
					}
					_, hasTools := body["tools"]
					if hasTools != test.wantTools {
						t.Errorf("upstream tools present=%t, want %t", hasTools, test.wantTools)
					}
					if _, hasChoice := body["tool_choice"]; hasChoice {
						t.Error("unsupported tool_choice was forwarded upstream")
					}
					if stream {
						_, _ = fmt.Fprintln(w, `{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":2,"eval_count":1}`)
					} else {
						_, _ = fmt.Fprint(w, `{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":2,"eval_count":1}`)
					}
				}))
				t.Cleanup(server.Close)
				request := openai.ChatCompletionRequest{
					Model: "test-model", Stream: stream, ToolChoice: test.choice,
					Messages: []openai.Message{{Role: "user", Content: "hello"}},
					Tools:    []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "lookup"}}},
				}
				client := NewOllama(server.URL, true)
				var err error
				if stream {
					_, err = client.StreamChatCompletions(context.Background(), request, func(string) error { return nil })
				} else {
					_, err = client.ChatCompletions(context.Background(), request)
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

func TestOllamaChatToolChoiceNoneRejectsUnexpectedToolCall(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, `{"model":"test-model","message":{"role":"assistant","tool_calls":[{"function":{"name":"lookup","arguments":{}}}]},"done":true,"prompt_eval_count":2,"eval_count":1}`)
				if stream {
					_, _ = fmt.Fprint(w, "\n")
				}
			}))
			t.Cleanup(server.Close)
			request := openai.ChatCompletionRequest{
				Model: "test-model", Stream: stream, ToolChoice: "none",
				Messages: []openai.Message{{Role: "user", Content: "hello"}},
				Tools:    []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "lookup"}}},
			}
			client := NewOllama(server.URL, true)
			var err error
			if stream {
				_, err = client.StreamChatCompletions(context.Background(), request, func(string) error { return nil })
			} else {
				_, err = client.ChatCompletions(context.Background(), request)
			}
			if err == nil || !strings.Contains(err.Error(), "invalid Ollama tool call") {
				t.Fatalf("expected unexpected tool-call rejection, got %v", err)
			}
		})
	}
}
