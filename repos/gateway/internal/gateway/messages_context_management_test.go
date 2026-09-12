package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestMessagesContextManagementValidation(t *testing.T) {
	var request messagesRequest
	valid := `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":{"type":"thinking_turns","value":2}},{"type":"clear_tool_uses_20250919","trigger":{"type":"input_tokens","value":30000},"keep":{"type":"tool_uses","value":3},"clear_at_least":{"type":"input_tokens","value":5000},"clear_tool_inputs":["shell"],"exclude_tools":["memory"]}]}}`
	if json.Unmarshal([]byte(valid), &request) != nil {
		t.Fatal("decode failed")
	}
	chat, err := request.chat()
	if err != nil || !strings.Contains(string(chat.AnthropicContextManagement), `"clear_thinking_20251015"`) || !strings.Contains(string(chat.AnthropicContextManagement), `"clear_tool_inputs":["shell"]`) {
		t.Fatalf("context management=%s err=%v", chat.AnthropicContextManagement, err)
	}
	for _, context := range []string{
		`{"edits":[]}`,
		`{"edits":[{"type":"unknown"}]}`,
		`{"edits":[{"type":"clear_tool_uses_20250919"},{"type":"clear_thinking_20251015"}]}`,
		`{"edits":[{"type":"clear_tool_uses_20250919","trigger":{"type":"input_tokens","value":0}}]}`,
		`{"edits":[{"type":"clear_tool_uses_20250919","unknown":true}]}`,
		`{"edits":[{"type":"clear_thinking_20251015","keep":{"type":"thinking_turns","value":0}}]}`,
	} {
		if json.Unmarshal([]byte(valid), &request) != nil {
			t.Fatal("decode failed")
		}
		if json.Unmarshal([]byte(context), &request.ContextManagement) != nil {
			t.Fatal("context decode failed")
		}
		if _, err := request.chat(); err == nil {
			t.Fatalf("invalid context management accepted: %s", context)
		}
	}
}

func TestMessagesContextManagementResponseJSONAndSSE(t *testing.T) {
	context := json.RawMessage(`{"applied_edits":[{"type":"clear_tool_uses_20250919","cleared_tool_uses":4,"cleared_input_tokens":8000}]}`)
	response := openai.ChatCompletionResponse{ID: "msg-1", Model: "m", NativeContextManagement: context, Choices: []openai.Choice{{Message: openai.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}}, Usage: openai.Usage{PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11}}
	payload, err := messagesResponsePayload(response)
	if err != nil || payload["context_management"] == nil {
		t.Fatalf("JSON payload=%v err=%v", payload, err)
	}
	recorder := httptest.NewRecorder()
	writer := &messagesWriter{destination: recorder, headers: make(map[string][]string), status: 200, tools: map[int]int{}}
	if err := writer.streamResult(response); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recorder.Body.String(), `"context_management":{"applied_edits"`) {
		t.Fatalf("SSE omitted context management: %s", recorder.Body.String())
	}
	response.NativeContextManagement = json.RawMessage(`{"applied_edits":[{"type":"clear_tool_uses_20250919","cleared_tool_uses":-1,"cleared_input_tokens":0}]}`)
	if _, err := messagesResponsePayload(response); err == nil {
		t.Fatal("invalid provider context result accepted")
	}
}
