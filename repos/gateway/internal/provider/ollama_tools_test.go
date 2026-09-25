package provider

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestOllamaRejectsUnsupportedChatToolSchemas(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected upstream call", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	strict := true
	for _, test := range []struct {
		name string
		tool openai.Tool
		want string
	}{
		{"strict function", openai.Tool{Type: "function", Function: openai.FunctionDefinition{Name: "lookup", Strict: &strict}}, "tools"},
		{"root additional properties", ollamaTestTool(map[string]any{"type": "object", "additionalProperties": false}), "tools.function.parameters"},
		{"nested const", ollamaTestTool(map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"type": "string", "const": "city"}}}), "tools.function.parameters"},
		{"nested oneOf", ollamaTestTool(map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"oneOf": []any{map[string]any{"type": "string"}}}}}), "tools.function.parameters"},
		{"root anyOf", ollamaTestTool(map[string]any{"anyOf": []any{map[string]any{"type": "object"}}}), "tools.function.parameters"},
		{"missing root type", ollamaTestTool(map[string]any{"properties": map[string]any{}}), "tools.function.parameters"},
		{"invalid root type", ollamaTestTool(map[string]any{"type": []any{"object"}}), "tools.function.parameters"},
		{"invalid required", ollamaTestTool(map[string]any{"type": "object", "required": "city"}), "tools.function.parameters"},
		{"invalid nested type", ollamaTestTool(map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": 7}}}), "tools.function.parameters"},
		{"invalid nested description", ollamaTestTool(map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"description": false}}}), "tools.function.parameters"},
		{"invalid nested enum", ollamaTestTool(map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"enum": "Moscow"}}}), "tools.function.parameters"},
		{"invalid items", ollamaTestTool(map[string]any{"type": "object", "properties": map[string]any{"cities": map[string]any{"type": "array", "items": "string"}}}), "tools.function.parameters"},
		{"invalid definitions", ollamaTestTool(map[string]any{"type": "object", "$defs": "city"}), "tools.function.parameters"},
		{"unknown tool type", openai.Tool{Type: "web_search", Function: openai.FunctionDefinition{Name: "search"}}, "tools"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "hello"}}, Tools: []openai.Tool{test.tool}}
			provider := NewOllama(server.URL, true)
			for _, run := range []func() error{
				func() error { _, err := provider.ChatCompletions(t.Context(), request); return err },
				func() error { _, err := provider.StreamChatCompletions(t.Context(), request, nil); return err },
			} {
				var failure *Error
				if err := run(); !errors.As(err, &failure) || failure.Class != FailureClientRequest || failure.Param != test.want {
					t.Fatalf("unsupported tool was not rejected for %s: %v", test.want, err)
				}
			}
		})
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("unsupported tools reached Ollama %d times", got)
	}
}

func TestOllamaAcceptsRepresentableChatToolSchema(t *testing.T) {
	tool := ollamaTestTool(map[string]any{
		"type": "object",
		"properties": map[string]any{"city": map[string]any{
			"anyOf":       []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
			"description": "City name", "enum": []any{"Moscow", nil},
		}},
		"required": []any{"city"},
		"$defs":    map[string]any{"unused": map[string]any{"type": "string"}},
	})
	if err := (Ollama{}).ValidateChatParameters(openai.ChatCompletionRequest{Tools: []openai.Tool{tool}}); err != nil {
		t.Fatalf("representable tool schema was rejected: %v", err)
	}
}

func ollamaTestTool(parameters map[string]any) openai.Tool {
	return openai.Tool{Type: "function", Function: openai.FunctionDefinition{Name: "lookup", Parameters: parameters}}
}
