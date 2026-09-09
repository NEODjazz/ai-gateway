package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

const cohereValidChatStream = "event: message-start\ndata: {\"type\":\"message-start\",\"id\":\"chat-stream\",\"delta\":{\"message\":{\"role\":\"assistant\"}}}\n\n" +
	"event: content-start\ndata: {\"type\":\"content-start\",\"index\":0,\"delta\":{\"message\":{\"content\":{\"type\":\"text\",\"text\":\"\"}}}}\n\n" +
	"event: content-delta\ndata: {\"type\":\"content-delta\",\"index\":0,\"delta\":{\"message\":{\"content\":{\"text\":\"hel\"}}}}\n\n" +
	"event: content-delta\ndata: {\"type\":\"content-delta\",\"index\":0,\"delta\":{\"message\":{\"content\":{\"text\":\"lo\"}}}}\n\n" +
	"event: content-end\ndata: {\"type\":\"content-end\",\"index\":0}\n\n" +
	"event: message-end\ndata: {\"type\":\"message-end\",\"delta\":{\"finish_reason\":\"COMPLETE\",\"usage\":{\"billed_units\":{\"input_tokens\":4,\"output_tokens\":2}}}}\n\n"

func TestCohereNativeChatStreamProtocolAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request cohereChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if r.URL.Path != "/v2/chat" || r.Header.Get("Authorization") != "Bearer key" || r.Header.Get("Accept") != "text/event-stream" || !request.Stream {
			t.Fatalf("invalid stream request: path=%s headers=%v body=%+v", r.URL.Path, r.Header, request)
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = fmt.Fprint(w, cohereValidChatStream)
	}))
	defer server.Close()

	var chunks []string
	response, err := NewCohere(server.URL, "key", true).StreamChatCompletions(context.Background(), openai.ChatCompletionRequest{Model: "command", Messages: []openai.Message{{Role: "user", Content: "hello"}}, Stream: true}, func(payload string) error {
		chunks = append(chunks, payload)
		return nil
	})
	joined := strings.Join(chunks, "")
	if err != nil || response.ID != "chat-stream" || response.Choices[0].Message.Content != "hello" || response.Choices[0].FinishReason != "stop" || response.Usage.TotalTokens != 6 || len(chunks) != 4 || !strings.Contains(joined, `"role":"assistant"`) || !strings.Contains(joined, `"finish_reason":"stop"`) {
		t.Fatalf("response=%+v chunks=%v err=%v", response, chunks, err)
	}
}

func TestCohereChatStreamRejectsMalformedLifecycle(t *testing.T) {
	for name, wire := range map[string]string{
		"missing end": strings.Replace(cohereValidChatStream, "event: message-end", "event: debug", 1),
		"bad index":   strings.Replace(cohereValidChatStream, `"index":0`, `"index":1`, 1),
		"tool event":  strings.Replace(cohereValidChatStream, "event: content-start", "event: tool-call-start", 1),
		"partial usage": strings.Replace(cohereValidChatStream,
			`"billed_units":{"input_tokens":4,"output_tokens":2}`, `"billed_units":{"input_tokens":4}`, 1),
		"trailing event": cohereValidChatStream + "event: debug\ndata: {\"type\":\"debug\"}\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := streamCohereChat(strings.NewReader(wire), "command", func(string) error { return nil }); err == nil {
				t.Fatal("malformed stream accepted")
			}
		})
	}
}

func TestCohereChatStreamHonorsConfigurationAndWriterFailure(t *testing.T) {
	request := openai.ChatCompletionRequest{Model: "command", Messages: []openai.Message{{Role: "user", Content: "hello"}}}
	if _, err := NewCohere("http://unused.invalid", "").StreamChatCompletions(context.Background(), request, func(string) error { return nil }); !errors.Is(err, ErrStreamingUnsupported) {
		t.Fatalf("disabled stream returned %v", err)
	}
	sentinel := errors.New("client disconnected")
	if _, err := streamCohereChat(strings.NewReader(cohereValidChatStream), "command", func(string) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("writer failure lost: %v", err)
	}
}

func TestCohereChatStreamRejectsNonSSESuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"type":"message-end"}`)
	}))
	defer server.Close()
	request := openai.ChatCompletionRequest{Model: "command", Messages: []openai.Message{{Role: "user", Content: "hello"}}}
	if _, err := NewCohere(server.URL, "", true).StreamChatCompletions(context.Background(), request, func(string) error { return nil }); err == nil || !strings.Contains(err.Error(), "non-SSE") {
		t.Fatalf("non-SSE response accepted: %v", err)
	}
}
