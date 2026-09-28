package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
		{"unknown schema draft", ollamaTestTool(map[string]any{"$schema": "https://example.test/schema", "type": "object"}), "tools.function.parameters"},
		{"nested schema draft", ollamaTestTool(map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "integer"}}}), "tools.function.parameters"},
		{"invalid minimum", ollamaTestTool(map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "integer", "minimum": "zero"}}}), "tools.function.parameters"},
		{"invalid exclusive minimum", ollamaTestTool(map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "integer", "exclusiveMinimum": true}}}), "tools.function.parameters"},
		{"impossible bounds", ollamaTestTool(map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "integer", "minimum": 10, "maximum": 1}}}), "tools.function.parameters"},
		{"impossible exclusive bounds", ollamaTestTool(map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "integer", "exclusiveMinimum": 1, "maximum": 1}}}), "tools.function.parameters"},
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

func TestOllamaChatForwardsOpenCodeStyleToolSchemas(t *testing.T) {
	const draft = "https://json-schema.org/draft/2020-12/schema"
	tools := make([]openai.Tool, 11)
	for index := range tools {
		tools[index] = openai.Tool{Type: "function", Function: openai.FunctionDefinition{
			Name: fmt.Sprintf("tool_%d", index),
			Parameters: map[string]any{
				"$schema":    draft,
				"type":       "object",
				"properties": map[string]any{"query": map[string]any{"type": "string"}},
			},
		}}
	}
	first := tools[0].Function.Parameters.(map[string]any)["properties"].(map[string]any)
	first["timeout"] = map[string]any{"type": "integer", "description": "Time limit", "minimum": -9007199254740991, "exclusiveMinimum": 0, "maximum": 9007199254740991}
	second := tools[1].Function.Parameters.(map[string]any)["properties"].(map[string]any)
	second["offset"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 9007199254740991}
	second["limit"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 9007199254740991}
	third := tools[2].Function.Parameters.(map[string]any)["properties"].(map[string]any)
	third["format"] = map[string]any{"type": "string", "default": "markdown"}

	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/chat" {
			t.Errorf("unexpected Ollama path %q", r.URL.Path)
		}
		var outbound ollamaChatRequest
		if err := json.NewDecoder(r.Body).Decode(&outbound); err != nil {
			t.Errorf("decode outbound request: %v", err)
		}
		if len(outbound.Tools) != 11 {
			t.Errorf("got %d tools, want 11", len(outbound.Tools))
		} else {
			for index, tool := range outbound.Tools {
				schema := tool.Function.Parameters.(map[string]any)
				if schema["$schema"] != draft {
					t.Errorf("tool %d lost schema draft", index)
				}
			}
			properties := outbound.Tools[0].Function.Parameters.(map[string]any)["properties"].(map[string]any)
			timeout := properties["timeout"].(map[string]any)
			if timeout["minimum"] != float64(-9007199254740991) || timeout["maximum"] != float64(9007199254740991) || timeout["exclusiveMinimum"] != float64(0) {
				t.Errorf("numeric constraints changed: %v", timeout)
			}
			description := timeout["description"].(string)
			for _, part := range []string{"Time limit", "minimum=-9007199254740991", "maximum=9007199254740991", "exclusiveMinimum=0"} {
				if !strings.Contains(description, part) {
					t.Errorf("description omitted %q: %q", part, description)
				}
			}
			format := outbound.Tools[2].Function.Parameters.(map[string]any)["properties"].(map[string]any)["format"].(map[string]any)
			if format["default"] != "markdown" || !strings.Contains(format["description"].(string), `default="markdown"`) {
				t.Errorf("default was not forwarded in model-visible form: %v", format)
			}
		}
		_, _ = w.Write([]byte(`{"model":"llama3.2:latest","message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop","prompt_eval_count":1,"eval_count":1}`))
	}))
	t.Cleanup(server.Close)
	request := openai.ChatCompletionRequest{Model: "llama3.2:latest", Messages: []openai.Message{{Role: "user", Content: "hello"}}, Tools: tools}
	if _, err := NewOllama(server.URL, false).ChatCompletions(t.Context(), request); err != nil {
		t.Fatalf("OpenCode-style tools did not reach Ollama adapter: %v", err)
	}
	if calls != 1 {
		t.Fatalf("adapter calls = %d, want 1", calls)
	}
	if _, ok := first["timeout"].(map[string]any)["description"].(string); !ok || first["timeout"].(map[string]any)["description"] != "Time limit" {
		t.Fatal("forwarding mutated the caller's schema")
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
