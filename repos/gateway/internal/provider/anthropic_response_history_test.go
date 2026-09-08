package provider

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestAnthropicResponsesToolHistory(t *testing.T) {
	input := []any{
		map[string]any{"role": "user", "content": "lookup"},
		map[string]any{"type": "function_call", "call_id": "a", "name": "lookup", "arguments": `{"id":9007199254740993}`},
		map[string]any{"type": "function_call", "call_id": "b", "name": "lookup", "arguments": `{}`},
		map[string]any{"type": "function_call_output", "call_id": "a", "output": "first"},
		map[string]any{"type": "function_call_output", "call_id": "b", "output": "second"},
	}
	request, err := anthropicResponsesRequest(openai.ResponseRequest{Input: input}, false)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Messages []struct {
			Role    string
			Content json.RawMessage
		}
	}
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 3 {
		t.Fatalf("messages=%s", encoded)
	}
	var calls []struct {
		Type, ID, Name string
		Input          map[string]json.RawMessage
	}
	if err := json.Unmarshal(body.Messages[1].Content, &calls); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0].ID != "a" || calls[1].ID != "b" || calls[0].Type != "tool_use" || string(calls[0].Input["id"]) != "9007199254740993" {
		t.Fatalf("calls=%s", body.Messages[1].Content)
	}
	var results []struct {
		Type      string
		ToolUseID string `json:"tool_use_id"`
		Content   string
	}
	if err := json.Unmarshal(body.Messages[2].Content, &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].ToolUseID != "a" || results[1].ToolUseID != "b" || results[1].Content != "second" || body.Messages[1].Role != "assistant" || body.Messages[2].Role != "user" {
		t.Fatalf("messages=%s", encoded)
	}
}

func TestAnthropicResponsesRejectsInvalidToolHistoryBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	for _, item := range []map[string]any{
		{"type": "function_call", "name": "tool", "arguments": "{}"},
		{"type": "function_call", "call_id": "a", "arguments": "{}"},
		{"type": "function_call", "call_id": "a", "name": "tool", "arguments": "[]"},
		{"type": "function_call", "call_id": "a", "name": "tool", "arguments": "null"},
		{"type": "function_call", "call_id": "a", "name": "tool", "arguments": "{broken"},
		{"type": "function_call_output", "call_id": "a"},
		{"type": "function_call_output", "call_id": "a", "output": 123},
	} {
		request := openai.ResponseRequest{Model: "m", Input: []any{item}}
		client := NewAnthropic(server.URL, "", true)
		for _, stream := range []bool{false, true} {
			var err error
			if stream {
				_, err = client.StreamResponses(t.Context(), request, func(string, string) error { return nil })
			} else {
				_, err = client.Responses(t.Context(), request)
			}
			var failure *Error
			if !errors.As(err, &failure) || failure.StatusCode != 400 || failure.Param != "input" {
				t.Errorf("item=%v stream=%v err=%v", item, stream, err)
			}
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls=%d", calls.Load())
	}
}

func TestAnthropicResponsesOrdersToolResultsBeforeUserText(t *testing.T) {
	input := []any{
		map[string]any{"type": "function_call", "call_id": "a", "name": "tool", "arguments": "{}"},
		map[string]any{"type": "function_call", "call_id": "b", "name": "tool", "arguments": "{}"},
		map[string]any{"role": "user", "content": "before"},
		map[string]any{"type": "function_call_output", "call_id": "a", "output": "A"},
		map[string]any{"role": "user", "content": "between"},
		map[string]any{"type": "function_call_output", "call_id": "b", "output": "B"},
		map[string]any{"role": "user", "content": "after"},
	}
	messages, err := anthropicResponseMessages(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages=%+v", messages)
	}
	blocks, ok := messages[1].Content.([]anthropicContent)
	if !ok || len(blocks) != 5 {
		t.Fatalf("content=%+v", messages[1].Content)
	}
	if blocks[0].Type != "tool_result" || blocks[0].ToolUseID != "a" || blocks[1].Type != "tool_result" || blocks[1].ToolUseID != "b" || blocks[2].Text != "before" || blocks[3].Text != "between" || blocks[4].Text != "after" {
		t.Fatalf("blocks=%+v", blocks)
	}
}
