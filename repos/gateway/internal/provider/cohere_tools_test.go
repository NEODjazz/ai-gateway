package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestCohereChatToolsJSONProtocolAndResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request cohereChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.Tools) != 1 || request.Tools[0].Function.Name != "weather" || request.Tools[0].Function.Strict != nil || request.ToolChoice != "REQUIRED" || !request.StrictTools || len(request.Messages) != 3 {
			t.Fatalf("tool request lost fields: %+v", request)
		}
		if len(request.Messages[1].ToolCalls) != 1 || request.Messages[1].ToolCalls[0].Function.Arguments != `{"city":"Paris"}` || request.Messages[2].ToolCallID != "call-old" {
			t.Fatalf("tool history lost: %+v", request.Messages)
		}
		content, ok := request.Messages[2].Content.([]any)
		if !ok || len(content) != 1 {
			t.Fatalf("tool result not encoded as document: %#v", request.Messages[2].Content)
		}
		_, _ = fmt.Fprint(w, `{"id":"chat-tool","finish_reason":"TOOL_CALL","message":{"role":"assistant","tool_calls":[{"id":"call-new","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Rome\"}"}}]},"usage":{"billed_units":{"input_tokens":9,"output_tokens":3}}}`)
	}))
	defer server.Close()

	strict, parallel := true, true
	call := openai.ToolCall{ID: "call-old", Type: "function", Function: openai.FunctionCall{Name: "weather", Arguments: `{"city":"Paris"}`}}
	response, err := NewCohere(server.URL, "key").ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model: "command", Messages: []openai.Message{{Role: "user", Content: "weather"}, {Role: "assistant", ToolCalls: []openai.ToolCall{call}}, {Role: "tool", ToolCallID: "call-old", Content: `{"temperature":20}`}},
		Tools: []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "weather", Description: "Get weather", Parameters: map[string]any{"type": "object"}, Strict: &strict}}}, ToolChoice: "required", ParallelToolCalls: &parallel,
	})
	if err != nil || response.Choices[0].FinishReason != "tool_calls" || len(response.Choices[0].Message.ToolCalls) != 1 || response.Choices[0].Message.ToolCalls[0].ID != "call-new" || response.Usage.TotalTokens != 12 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestCohereChatToolsRejectLossyRequestsBeforeUpstream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("invalid tools reached upstream") }))
	defer server.Close()
	strict, relaxed, disabled := true, false, false
	tool := openai.Tool{Type: "function", Function: openai.FunctionDefinition{Name: "weather", Parameters: map[string]any{"type": "object"}, Strict: &strict}}
	valid := openai.ChatCompletionRequest{Model: "command", Messages: []openai.Message{{Role: "user", Content: "weather"}}, Tools: []openai.Tool{tool}}
	for param, mutate := range map[string]func(*openai.ChatCompletionRequest){
		"parallel_tool_calls": func(r *openai.ChatCompletionRequest) { r.ParallelToolCalls = &disabled },
		"tool_choice": func(r *openai.ChatCompletionRequest) {
			r.ToolChoice = map[string]any{"type": "function", "function": map[string]any{"name": "weather"}}
		},
		"tools": func(r *openai.ChatCompletionRequest) {
			other := tool
			other.Function.Name, other.Function.Strict = "other", &relaxed
			r.Tools = append(r.Tools, other)
		},
		"messages": func(r *openai.ChatCompletionRequest) {
			r.Messages = append(r.Messages, openai.Message{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "call", Type: "function", Function: openai.FunctionCall{Name: "weather", Arguments: "invalid"}}}})
		},
	} {
		request := valid
		request.Messages = append([]openai.Message(nil), valid.Messages...)
		request.Tools = append([]openai.Tool(nil), valid.Tools...)
		mutate(&request)
		_, err := NewCohere(server.URL, "").ChatCompletions(context.Background(), request)
		var failure *Error
		if !errors.As(err, &failure) || failure.Param != param {
			t.Fatalf("%s: %v", param, err)
		}
	}
}
