package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestOllamaRejectsInvalidNativeToolCalls(t *testing.T) {
	tests := []struct {
		name  string
		tools []openai.Tool
		calls string
	}{
		{name: "unrequested", calls: `[{"function":{"name":"weather.get","arguments":{}}}]`},
		{name: "undeclared", tools: []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "weather.get"}}}, calls: `[{"function":{"name":"secret.lookup","arguments":{}}}]`},
		{name: "missing name", tools: []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "weather.get"}}}, calls: `[{"function":{"arguments":{}}}]`},
		{name: "invalid arguments", tools: []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "weather.get"}}}, calls: `[{"function":{"name":"weather.get","arguments":[1]}}]`},
		{name: "duplicate ID", tools: []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "weather.get"}}}, calls: `[{"id":"duplicate","function":{"name":"weather.get","arguments":{}}},{"id":"duplicate","function":{"name":"weather.get","arguments":{}}}]`},
	}
	for _, test := range tests {
		for _, stream := range []bool{false, true} {
			name := test.name + "/json"
			if stream {
				name = test.name + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if stream {
						_, _ = fmt.Fprintf(w, `{"model":"test-model","message":{"role":"assistant","tool_calls":%s}}`+"\n", test.calls)
						_, _ = fmt.Fprintln(w, `{"model":"test-model","done":true,"prompt_eval_count":2,"eval_count":1}`)
						return
					}
					_, _ = fmt.Fprintf(w, `{"model":"test-model","message":{"role":"assistant","tool_calls":%s},"done":true,"prompt_eval_count":2,"eval_count":1}`, test.calls)
				}))
				t.Cleanup(server.Close)
				request := openai.ChatCompletionRequest{Model: "test-model", Stream: stream, Messages: []openai.Message{{Role: "user", Content: "weather"}}, Tools: test.tools}
				var err error
				var payloads []string
				if stream {
					_, err = NewOllama(server.URL, true).StreamChatCompletions(context.Background(), request, func(payload string) error {
						payloads = append(payloads, payload)
						return nil
					})
				} else {
					_, err = NewOllama(server.URL, false).ChatCompletions(context.Background(), request)
				}
				if err == nil || !strings.Contains(err.Error(), "invalid Ollama tool call") {
					t.Fatalf("expected invalid Ollama tool call, got %v", err)
				}
				if len(payloads) != 0 {
					t.Fatalf("invalid tool call was streamed: %v", payloads)
				}
			})
		}
	}
}

func TestOllamaRejectsDuplicateNativeToolCallAcrossStreamChunks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for range 2 {
			_, _ = fmt.Fprintln(w, `{"model":"test-model","message":{"role":"assistant","tool_calls":[{"id":"duplicate","function":{"name":"weather.get","arguments":{}}}]}}`)
		}
		_, _ = fmt.Fprintln(w, `{"model":"test-model","done":true,"prompt_eval_count":2,"eval_count":1}`)
	}))
	t.Cleanup(server.Close)
	request := openai.ChatCompletionRequest{
		Model: "test-model", Stream: true,
		Messages: []openai.Message{{Role: "user", Content: "weather"}},
		Tools:    []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "weather.get"}}},
	}
	var payloads []string
	_, err := NewOllama(server.URL, true).StreamChatCompletions(context.Background(), request, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "invalid Ollama tool call") {
		t.Fatalf("expected duplicate tool call rejection, got %v", err)
	}
	if len(payloads) != 1 {
		t.Fatalf("unexpected streamed tool calls: %v", payloads)
	}
}

func TestOllamaBoundsNativeToolCalls(t *testing.T) {
	calls := make([]openai.ToolCall, maxChatStreamToolCalls+1)
	if err := validateOllamaResponseToolCalls(calls, nil, nil); err == nil || !strings.Contains(err.Error(), "tool call count") {
		t.Fatalf("expected tool call count rejection, got %v", err)
	}
}
