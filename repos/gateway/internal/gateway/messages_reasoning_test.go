package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestMessagesReasoningHistoryRoundTrip(t *testing.T) {
	var request messagesRequest
	if err := json.Unmarshal([]byte(`{"model":"m","max_tokens":10,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"plan","signature":"signed"},{"type":"redacted_thinking","data":"opaque"},{"type":"text","text":"answer"}]},{"role":"user","content":"continue"}]}`), &request); err != nil {
		t.Fatal(err)
	}
	chat, err := request.chat()
	if err != nil {
		t.Fatal(err)
	}
	if len(chat.Messages[0].Reasoning) != 2 || chat.Messages[0].Reasoning[0].Signature != "signed" {
		t.Fatalf("reasoning history lost: %+v", chat.Messages[0])
	}
	content, err := messagesContent(openai.Message{Role: "assistant", Content: "result", Reasoning: chat.Messages[0].Reasoning})
	if err != nil || len(content) != 3 || content[0].(map[string]any)["type"] != "thinking" || content[1].(map[string]any)["type"] != "redacted_thinking" {
		t.Fatalf("native content conversion failed: content=%+v err=%v", content, err)
	}
}

func TestMessagesWriterStreamsReasoningBlocks(t *testing.T) {
	destination := httptest.NewRecorder()
	writer := &messagesWriter{destination: destination, headers: make(http.Header), status: http.StatusOK, tools: map[int]int{}}
	writer.headers.Set("Content-Type", "text/event-stream")
	chunks := []string{
		`{"id":"m","model":"model","choices":[{"index":0,"delta":{"reasoning":[{"index":0,"type":"thinking"}]}}]}`,
		`{"id":"m","model":"model","choices":[{"index":0,"delta":{"reasoning":[{"index":0,"type":"thinking","thinking":"private plan"}]}}]}`,
		`{"id":"m","model":"model","choices":[{"index":0,"delta":{"reasoning":[{"index":0,"type":"thinking","signature":"signed"}]}}]}`,
		`{"id":"m","model":"model","choices":[{"index":0,"delta":{"reasoning":[{"index":1,"type":"redacted_thinking","data":"opaque"}]}}]}`,
		`{"id":"m","model":"model","choices":[{"index":0,"delta":{"content":"result"},"finish_reason":"stop"}]}`,
		`{"id":"m","model":"model","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":6,"total_tokens":10,"completion_tokens_details":{"reasoning_tokens":3}}}`,
		`[DONE]`,
	}
	for _, chunk := range chunks {
		if _, err := writer.Write([]byte("data: " + chunk + "\n\n")); err != nil {
			t.Fatal(err)
		}
	}
	body := destination.Body.String()
	for _, expected := range []string{`"type":"thinking"`, `"type":"thinking_delta"`, `"thinking":"private plan"`, `"type":"signature_delta"`, `"signature":"signed"`, `"type":"redacted_thinking"`, `"data":"opaque"`, `"thinking_tokens":3`, "event: message_stop"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %s in %s", expected, body)
		}
	}
}

func TestMessagesThinkingConfiguration(t *testing.T) {
	for _, test := range []struct {
		name     string
		body     string
		typeName string
		budget   int
		display  string
	}{
		{name: "adaptive", body: `{"model":"m","max_tokens":4096,"messages":[{"role":"user","content":"work"}],"thinking":{"type":"adaptive","display":"summarized"}}`, typeName: "adaptive", display: "summarized"},
		{name: "enabled", body: `{"model":"m","max_tokens":4096,"messages":[{"role":"user","content":"work"}],"thinking":{"type":"enabled","budget_tokens":2048,"display":"omitted"},"temperature":1}`, typeName: "enabled", budget: 2048, display: "omitted"},
		{name: "disabled", body: `{"model":"m","max_tokens":4096,"messages":[{"role":"user","content":"work"}],"thinking":{"type":"disabled"},"temperature":0.2}`, typeName: "disabled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var request messagesRequest
			if err := json.Unmarshal([]byte(test.body), &request); err != nil {
				t.Fatal(err)
			}
			chat, err := request.chat()
			if err != nil {
				t.Fatal(err)
			}
			if chat.AnthropicThinking == nil || chat.AnthropicThinking.Type != test.typeName || chat.AnthropicThinking.Display != test.display {
				t.Fatalf("thinking=%+v", chat.AnthropicThinking)
			}
			if test.budget == 0 && chat.AnthropicThinking.BudgetTokens != nil || test.budget != 0 && (chat.AnthropicThinking.BudgetTokens == nil || *chat.AnthropicThinking.BudgetTokens != test.budget) {
				t.Fatalf("budget=%v", chat.AnthropicThinking.BudgetTokens)
			}
		})
	}
}

func TestMessagesThinkingRejectsInvalidConfiguration(t *testing.T) {
	for _, body := range []string{
		`{"type":"adaptive","budget_tokens":1024}`,
		`{"type":"enabled"}`,
		`{"type":"enabled","budget_tokens":1023}`,
		`{"type":"enabled","budget_tokens":4096}`,
		`{"type":"disabled","display":"omitted"}`,
		`{"type":"adaptive","display":"full"}`,
		`{"type":"unknown"}`,
	} {
		var request messagesRequest
		if err := json.Unmarshal([]byte(`{"model":"m","max_tokens":4096,"messages":[{"role":"user","content":"work"}],"thinking":`+body+`,"temperature":0.5}`), &request); err != nil {
			t.Fatal(err)
		}
		if _, err := request.chat(); err == nil {
			t.Fatalf("accepted thinking=%s", body)
		}
	}
}
