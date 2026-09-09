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
