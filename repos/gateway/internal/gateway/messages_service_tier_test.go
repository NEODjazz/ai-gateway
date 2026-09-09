package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestMessagesServiceTierRoundTrip(t *testing.T) {
	var request messagesRequest
	if err := json.Unmarshal([]byte(`{"model":"m","max_tokens":10,"service_tier":"standard_only","messages":[{"role":"user","content":"hello"}]}`), &request); err != nil {
		t.Fatal(err)
	}
	chat, err := request.chat()
	if err != nil || chat.ServiceTier != "standard_only" {
		t.Fatalf("request service tier lost: request=%+v err=%v", chat, err)
	}
	response := openai.ChatCompletionResponse{ID: "msg", Model: "m", ServiceTier: "priority", Choices: []openai.Choice{{Message: openai.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}}, Usage: openai.Usage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3}}
	payload, _ := json.Marshal(response)
	destination := httptest.NewRecorder()
	writer := &messagesWriter{destination: destination, headers: make(http.Header), status: http.StatusOK, tools: map[int]int{}}
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	writer.finish()
	if destination.Code != http.StatusOK || !strings.Contains(destination.Body.String(), `"service_tier":"priority"`) {
		t.Fatalf("response service tier lost: %s", destination.Body.String())
	}
}

func TestMessagesStreamPreservesAssignedServiceTier(t *testing.T) {
	destination := httptest.NewRecorder()
	writer := &messagesWriter{destination: destination, headers: make(http.Header), status: http.StatusOK, tools: map[int]int{}}
	writer.headers.Set("Content-Type", "text/event-stream")
	chunks := []string{
		`{"id":"m","model":"model","service_tier":"standard","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"id":"m","model":"model","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
		`{"id":"m","model":"model","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`,
		`[DONE]`,
	}
	for _, chunk := range chunks {
		if _, err := writer.Write([]byte("data: " + chunk + "\n\n")); err != nil {
			t.Fatal(err)
		}
	}
	if got := destination.Body.String(); strings.Count(got, `"service_tier":"standard"`) < 2 {
		t.Fatalf("stream service tier lost: %s", got)
	}
}

func TestMessagesRejectsUnknownServiceTier(t *testing.T) {
	var request messagesRequest
	if err := json.Unmarshal([]byte(`{"model":"m","max_tokens":10,"service_tier":"priority","messages":[{"role":"user","content":"hello"}]}`), &request); err != nil {
		t.Fatal(err)
	}
	if _, err := request.chat(); err == nil {
		t.Fatal("unknown Messages service tier was accepted")
	}
}

func TestMessagesRejectsInvalidOrChangingReportedServiceTier(t *testing.T) {
	for _, chunks := range [][]string{
		{`{"id":"m","model":"model","service_tier":"unknown","choices":[{"index":0,"delta":{"role":"assistant"}}]}`},
		{`{"id":"m","model":"model","service_tier":"standard","choices":[{"index":0,"delta":{"role":"assistant"}}]}`, `{"id":"m","model":"model","service_tier":"priority","choices":[]}`},
	} {
		destination := httptest.NewRecorder()
		writer := &messagesWriter{destination: destination, headers: make(http.Header), status: http.StatusOK, tools: map[int]int{}}
		writer.headers.Set("Content-Type", "text/event-stream")
		var err error
		for _, chunk := range chunks {
			if _, err = writer.Write([]byte("data: " + chunk + "\n\n")); err != nil {
				break
			}
		}
		if err == nil {
			t.Fatalf("invalid service tier stream was accepted: %+v", chunks)
		}
	}
}
