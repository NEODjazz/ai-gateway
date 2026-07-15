package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestAnthropicChatCompletions(t *testing.T) {
	var upstreamRequest anthropicRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "test-key" {
			t.Fatalf("expected anthropic api key header")
		}
		if r.Header.Get("Anthropic-Version") == "" {
			t.Fatalf("expected anthropic version header")
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(anthropicResponse{
			ID:         "msg-test",
			Type:       "message",
			Role:       "assistant",
			Model:      upstreamRequest.Model,
			StopReason: "end_turn",
			Content: []anthropicContent{
				{Type: "text", Text: "hello"},
			},
			Usage: anthropicUsage{InputTokens: 3, OutputTokens: 2},
		})
	}))
	defer server.Close()

	maxTokens := 77
	temperature := 0.3
	topP := 0.8
	provider := NewAnthropic(server.URL, "test-key", false)
	response, err := provider.ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model:       "claude-test",
		MaxTokens:   &maxTokens,
		Temperature: &temperature,
		TopP:        &topP,
		Messages: []openai.Message{
			{Role: "system", Content: "be concise"},
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if upstreamRequest.System != "be concise" {
		t.Fatalf("unexpected system prompt: %q", upstreamRequest.System)
	}
	if upstreamRequest.MaxTokens != 77 || upstreamRequest.Temperature == nil || *upstreamRequest.Temperature != temperature || upstreamRequest.TopP == nil || *upstreamRequest.TopP != topP {
		t.Fatalf("generation options were not forwarded: %+v", upstreamRequest)
	}
	if len(upstreamRequest.Messages) != 1 || upstreamRequest.Messages[0].Role != "user" || upstreamRequest.Messages[0].Content != "hi" {
		t.Fatalf("unexpected upstream messages: %+v", upstreamRequest.Messages)
	}
	if openai.ContentText(response.Choices[0].Message.Content) != "hello" {
		t.Fatalf("unexpected response content: %+v", response)
	}
	if response.Usage.TotalTokens != 5 {
		t.Fatalf("unexpected usage: %+v", response.Usage)
	}
}

func TestAnthropicStreamsChatCompletions(t *testing.T) {
	var upstreamRequest anthropicRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_start","message":{"id":"msg-test","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":3}}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_stop"}` + "\n\n"))
	}))
	defer server.Close()

	var payloads []string
	provider := NewAnthropic(server.URL, "test-key", true)
	response, err := provider.StreamChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model:  "claude-test",
		Stream: true,
		Messages: []openai.Message{
			{Role: "user", Content: "hi"},
		},
	}, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !upstreamRequest.Stream {
		t.Fatal("expected upstream stream to be enabled")
	}
	if len(payloads) != 3 {
		t.Fatalf("expected three chat payloads, got %d: %v", len(payloads), payloads)
	}
	if !strings.Contains(payloads[0], `"content":"hel"`) || !strings.Contains(payloads[1], `"content":"lo"`) || !strings.Contains(payloads[2], `"finish_reason":"stop"`) {
		t.Fatalf("unexpected payloads: %v", payloads)
	}
	if openai.ContentText(response.Choices[0].Message.Content) != "hello" {
		t.Fatalf("unexpected streamed response: %+v", response)
	}
}

func TestAnthropicResponses(t *testing.T) {
	var upstreamRequest anthropicRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(anthropicResponse{
			ID:         "msg-test",
			Type:       "message",
			Role:       "assistant",
			Model:      upstreamRequest.Model,
			StopReason: "end_turn",
			Content: []anthropicContent{
				{Type: "text", Text: "pong"},
			},
			Usage: anthropicUsage{InputTokens: 4, OutputTokens: 1},
		})
	}))
	defer server.Close()

	maxOutputTokens := 12
	provider := NewAnthropic(server.URL, "test-key", false)
	response, err := provider.Responses(context.Background(), openai.ResponseRequest{
		Model:           "claude-test",
		Instructions:    "answer shortly",
		Input:           "ping",
		MaxOutputTokens: &maxOutputTokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	if upstreamRequest.System != "answer shortly" || upstreamRequest.MaxTokens != 12 {
		t.Fatalf("unexpected upstream responses request: %+v", upstreamRequest)
	}
	if len(upstreamRequest.Messages) != 1 || upstreamRequest.Messages[0].Content != "ping" {
		t.Fatalf("unexpected upstream messages: %+v", upstreamRequest.Messages)
	}
	if response.OutputText != "pong" {
		t.Fatalf("unexpected output_text: %s", response.OutputText)
	}
	if response.Usage.TotalTokens != 5 {
		t.Fatalf("unexpected usage: %+v", response.Usage)
	}
}

func TestAnthropicStreamsResponses(t *testing.T) {
	var upstreamRequest anthropicRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_start","message":{"id":"msg-test","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":4}}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"po"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ng"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_stop"}` + "\n\n"))
	}))
	defer server.Close()

	var events []string
	var payloads []string
	provider := NewAnthropic(server.URL, "test-key", true)
	response, err := provider.StreamResponses(context.Background(), openai.ResponseRequest{
		Model:  "claude-test",
		Input:  "ping",
		Stream: true,
	}, func(event string, payload string) error {
		events = append(events, event)
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !upstreamRequest.Stream {
		t.Fatal("expected upstream stream to be enabled")
	}
	if len(payloads) != 4 {
		t.Fatalf("expected four responses payloads, got %d: %v", len(payloads), payloads)
	}
	if events[0] != "response.created" || events[1] != "response.output_text.delta" || events[3] != "response.completed" {
		t.Fatalf("unexpected events: %v", events)
	}
	if response.OutputText != "pong" {
		t.Fatalf("unexpected streamed output_text: %s", response.OutputText)
	}
}
