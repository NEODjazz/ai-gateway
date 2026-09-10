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

const cohereValidToolStream = "event: message-start\ndata: {\"type\":\"message-start\",\"id\":\"chat-tools\",\"delta\":{\"message\":{\"role\":\"assistant\"}}}\n\n" +
	"event: tool-plan-delta\ndata: {\"type\":\"tool-plan-delta\",\"delta\":{\"message\":{\"tool_plan\":\"Checking both cities\"}}}\n\n" +
	"event: tool-call-start\ndata: {\"type\":\"tool-call-start\",\"index\":7,\"delta\":{\"message\":{\"tool_calls\":{\"id\":\"call-paris\",\"type\":\"function\",\"function\":{\"name\":\"weather\",\"arguments\":\"\"}}}}}\n\n" +
	"event: tool-call-delta\ndata: {\"type\":\"tool-call-delta\",\"index\":7,\"delta\":{\"message\":{\"tool_calls\":{\"function\":{\"arguments\":\"{\\\"city\\\":\"}}}}}\n\n" +
	"event: tool-call-delta\ndata: {\"type\":\"tool-call-delta\",\"index\":7,\"delta\":{\"message\":{\"tool_calls\":{\"function\":{\"arguments\":\"\\\"Paris\\\"}\"}}}}}\n\n" +
	"event: tool-call-end\ndata: {\"type\":\"tool-call-end\",\"index\":7}\n\n" +
	"event: tool-call-start\ndata: {\"type\":\"tool-call-start\",\"index\":11,\"delta\":{\"message\":{\"tool_calls\":{\"id\":\"call-rome\",\"type\":\"function\",\"function\":{\"name\":\"weather\",\"arguments\":\"\"}}}}}\n\n" +
	"event: tool-call-delta\ndata: {\"type\":\"tool-call-delta\",\"index\":11,\"delta\":{\"message\":{\"tool_calls\":{\"function\":{\"arguments\":\"{\\\"city\\\":\\\"Rome\\\"}\"}}}}}\n\n" +
	"event: tool-call-end\ndata: {\"type\":\"tool-call-end\",\"index\":11}\n\n" +
	"event: message-end\ndata: {\"type\":\"message-end\",\"delta\":{\"finish_reason\":\"TOOL_CALL\",\"usage\":{\"billed_units\":{\"input_tokens\":18,\"output_tokens\":9}}}}\n\n"

func TestCohereNativeChatStreamProtocolAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request cohereChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if r.URL.Path != "/v2/chat" || r.Header.Get("Authorization") != "Bearer key" || r.Header.Get("Accept") != "text/event-stream" || !request.Stream || request.K == nil || *request.K != 20 {
			t.Fatalf("invalid stream request: path=%s headers=%v body=%+v", r.URL.Path, r.Header, request)
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = fmt.Fprint(w, cohereValidChatStream)
	}))
	defer server.Close()

	var chunks []string
	topK := 20
	response, err := NewCohere(server.URL, "key", true).StreamChatCompletions(context.Background(), openai.ChatCompletionRequest{Model: "command", Messages: []openai.Message{{Role: "user", Content: "hello"}}, Stream: true, ChatGenerationOptions: openai.ChatGenerationOptions{TopK: &topK}}, func(payload string) error {
		chunks = append(chunks, payload)
		return nil
	})
	joined := strings.Join(chunks, "")
	if err != nil || response.ID != "chat-stream" || response.Choices[0].Message.Content != "hello" || response.Choices[0].FinishReason != "stop" || response.Usage.TotalTokens != 6 || len(chunks) != 4 || !strings.Contains(joined, `"role":"assistant"`) || !strings.Contains(joined, `"finish_reason":"stop"`) {
		t.Fatalf("response=%+v chunks=%v err=%v", response, chunks, err)
	}
}

func TestCohereNativeParallelToolStreamProtocolAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request cohereChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if !request.Stream || len(request.Tools) != 1 || request.ToolChoice != "REQUIRED" {
			t.Fatalf("invalid tool stream request: %+v", request)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, cohereValidToolStream)
	}))
	defer server.Close()

	var chunks []string
	response, err := NewCohere(server.URL, "", true).StreamChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model: "command", Messages: []openai.Message{{Role: "user", Content: "weather"}}, Stream: true,
		Tools: []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "weather", Parameters: map[string]any{"type": "object"}}}}, ToolChoice: "required",
	}, func(payload string) error {
		chunks = append(chunks, payload)
		return nil
	})
	joined := strings.Join(chunks, "")
	if err != nil || response.ID != "chat-tools" || response.Choices[0].FinishReason != "tool_calls" || response.Usage.TotalTokens != 27 || len(response.Choices[0].Message.ToolCalls) != 2 {
		t.Fatalf("response=%+v chunks=%v err=%v", response, chunks, err)
	}
	first, second := response.Choices[0].Message.ToolCalls[0], response.Choices[0].Message.ToolCalls[1]
	if first.ID != "call-paris" || first.Function.Arguments != `{"city":"Paris"}` || second.ID != "call-rome" || second.Function.Arguments != `{"city":"Rome"}` || !strings.Contains(joined, `"index":0`) || !strings.Contains(joined, `"index":1`) || !strings.Contains(joined, `"finish_reason":"tool_calls"`) {
		t.Fatalf("tool stream mismatch: response=%+v chunks=%s", response, joined)
	}
}

func TestCohereChatStreamRejectsMalformedLifecycle(t *testing.T) {
	for name, wire := range map[string]string{
		"missing end": strings.Replace(cohereValidChatStream, "event: message-end", "event: debug", 1),
		"bad index":   strings.Replace(cohereValidChatStream, `"index":0`, `"index":1`, 1),
		"tool event":  strings.Replace(cohereValidChatStream, "event: content-start", "event: tool-call-start", 1),
		"partial usage": strings.Replace(cohereValidChatStream,
			`"billed_units":{"input_tokens":4,"output_tokens":2}`, `"billed_units":{"input_tokens":4}`, 1),
		"trailing event":          cohereValidChatStream + "event: debug\ndata: {\"type\":\"debug\"}\n\n",
		"tool delta before start": strings.Replace(cohereValidToolStream, "event: tool-call-start", "event: debug", 1),
		"duplicate tool index":    strings.Replace(cohereValidToolStream, `"index":11`, `"index":7`, 1),
		"unfinished tool call":    strings.Replace(cohereValidToolStream, "event: tool-call-end", "event: debug", 1),
		"wrong tool finish":       strings.Replace(cohereValidToolStream, `"finish_reason":"TOOL_CALL"`, `"finish_reason":"COMPLETE"`, 1),
		"invalid tool arguments":  strings.Replace(cohereValidToolStream, `\"Paris\"}`, `Paris}`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := streamCohereChat(strings.NewReader(wire), "command", func(string) error { return nil }); err == nil {
				t.Fatal("malformed stream accepted")
			}
		})
	}
}

func TestCohereChatStreamLimitsToolArguments(t *testing.T) {
	delta, err := json.Marshal(map[string]any{"type": "tool-call-delta", "index": 0, "delta": map[string]any{"message": map[string]any{"tool_calls": map[string]any{"function": map[string]any{"arguments": strings.Repeat("x", openai.MaxChatFunctionArgumentsChars+1)}}}}})
	if err != nil {
		t.Fatal(err)
	}
	oversized := "event: message-start\ndata: {\"type\":\"message-start\",\"id\":\"chat-tools\",\"delta\":{\"message\":{\"role\":\"assistant\"}}}\n\n" +
		"event: tool-call-start\ndata: {\"type\":\"tool-call-start\",\"index\":0,\"delta\":{\"message\":{\"tool_calls\":{\"id\":\"call\",\"type\":\"function\",\"function\":{\"name\":\"weather\",\"arguments\":\"\"}}}}}\n\n" +
		"event: tool-call-delta\ndata: " + string(delta) + "\n\n"
	if _, err := streamCohereChat(strings.NewReader(oversized), "command", func(string) error { return nil }); err == nil {
		t.Fatalf("oversized arguments accepted: %v", err)
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
