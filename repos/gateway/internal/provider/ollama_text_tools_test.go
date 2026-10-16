package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestPromoteOllamaTextToolCall(t *testing.T) {
	tools := []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "question"}}}
	for _, test := range []struct {
		name, content string
		wantCall      bool
	}{
		{"template output", `{"name":"question","parameters":{"questions":[{"header":"What can I do?","options":["Answer","Search"]}]}}`, true},
		{"unknown function", `{"name":"delete","parameters":{}}`, false},
		{"unknown field", `{"name":"question","parameters":{},"extra":true}`, false},
		{"invalid parameters", `{"name":"question","parameters":[]}`, false},
		{"explanatory text", `Example: {"name":"question","parameters":{}}`, false},
		{"ordinary JSON answer", `{"name":"question","description":"an example"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := openai.Message{Role: "assistant", Content: test.content}
			promoteOllamaTextToolCall(&message, tools)
			if got := len(message.ToolCalls) == 1; got != test.wantCall {
				t.Fatalf("tool call=%t, want %t: %+v", got, test.wantCall, message)
			}
			if test.wantCall {
				if message.Content != "" || message.ToolCalls[0].Function.Name != "question" {
					t.Fatalf("invalid promoted call: %+v", message)
				}
			} else if message.Content != test.content {
				t.Fatalf("non-call content changed: %+v", message)
			}
		})
	}
}

func TestOllamaChatConvertsLlama32TextToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var outbound ollamaChatRequest
		if err := json.NewDecoder(r.Body).Decode(&outbound); err != nil || outbound.Stream {
			t.Errorf("expected non-streaming Ollama request: stream=%t err=%v", outbound.Stream, err)
		}
		_, _ = w.Write([]byte(`{"model":"llama3.2:latest","message":{"role":"assistant","content":"{\"name\":\"question\",\"parameters\":{\"questions\":[{\"header\":\"What can I do?\",\"options\":[\"Answer\",\"Search\"]}]}}"},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`))
	}))
	t.Cleanup(server.Close)
	request := openai.ChatCompletionRequest{
		Model: "llama3.2:latest", Messages: []openai.Message{{Role: "user", Content: "What can you do?"}},
		Tools: []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "question", Parameters: map[string]any{"type": "object"}}}},
	}
	response, err := NewOllama(server.URL, true).ChatCompletions(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	choice := response.Choices[0]
	if choice.FinishReason != "tool_calls" || choice.Message.Content != "" || len(choice.Message.ToolCalls) != 1 || choice.Message.ToolCalls[0].Function.Name != "question" || !strings.HasPrefix(choice.Message.ToolCalls[0].ID, "call_") {
		t.Fatalf("text tool call was not converted: %+v", choice)
	}
	if !strings.Contains(choice.Message.ToolCalls[0].Function.Arguments, `"questions"`) {
		t.Fatalf("tool arguments lost: %+v", choice.Message.ToolCalls[0])
	}
}
