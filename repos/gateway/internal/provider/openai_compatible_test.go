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

func TestProviderURLDoesNotDuplicateV1(t *testing.T) {
	got := providerURL("https://example.test/openai/v1", "chat/completions")
	want := "https://example.test/openai/v1/chat/completions"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestProviderURLAddsV1ForRootBaseURL(t *testing.T) {
	got := providerURL("https://example.test", "responses")
	want := "https://example.test/v1/responses"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestOpenAICompatibleDoesNotForwardStreamWhenDisabled(t *testing.T) {
	var upstreamRequest openAICompatibleChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
			ID:     "chatcmpl-test",
			Object: "chat.completion",
			Model:  upstreamRequest.Model,
			Choices: []openai.Choice{
				{Index: 0, Message: openai.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"},
			},
		})
	}))
	defer server.Close()

	provider := NewOpenAICompatible(server.URL, "", false)
	response, err := provider.ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model:  "test-model",
		Stream: true,
		Messages: []openai.Message{
			{Role: "user", Content: "hello"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if upstreamRequest.Stream {
		t.Fatal("expected upstream stream to be disabled")
	}
	if openai.ContentText(response.Choices[0].Message.Content) != "ok" {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestOpenAICompatibleCollectsChatStreamWhenEnabled(t *testing.T) {
	var upstreamRequest openAICompatibleChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl-test","model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","content":"hel"},"finish_reason":null}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl-test","model":"test-model","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	provider := NewOpenAICompatible(server.URL, "", true)
	response, err := provider.ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model:  "test-model",
		Stream: true,
		Messages: []openai.Message{
			{Role: "user", Content: "hello"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !upstreamRequest.Stream {
		t.Fatal("expected upstream stream to be enabled")
	}
	if openai.ContentText(response.Choices[0].Message.Content) != "hello" {
		t.Fatalf("expected collected stream content, got %+v", response.Choices[0].Message.Content)
	}
}

func TestOpenAICompatibleStreamsChatPayloadsWhenEnabled(t *testing.T) {
	var upstreamRequest openAICompatibleChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl-test","model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","content":"hel"},"finish_reason":null}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl-test","model":"test-model","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	var payloads []string
	provider := NewOpenAICompatible(server.URL, "", true)
	response, err := provider.StreamChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model:  "test-model",
		Stream: true,
		Messages: []openai.Message{
			{Role: "user", Content: "hello"},
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
	if len(payloads) != 2 {
		t.Fatalf("expected two streamed payloads, got %d: %v", len(payloads), payloads)
	}
	if !strings.Contains(payloads[0], `"content":"hel"`) || !strings.Contains(payloads[1], `"content":"lo"`) {
		t.Fatalf("unexpected streamed payloads: %v", payloads)
	}
	if openai.ContentText(response.Choices[0].Message.Content) != "hello" {
		t.Fatalf("expected collected stream content, got %+v", response.Choices[0].Message.Content)
	}
}

func TestOpenAICompatibleStreamsResponsesWhenEnabled(t *testing.T) {
	var upstreamRequest openAICompatibleResponseRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: response.created` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp-test","object":"response","status":"in_progress","model":"test-model","output":[]}}` + "\n\n"))
		_, _ = w.Write([]byte(`event: response.output_text.delta` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","response_id":"resp-test","delta":"hel"}` + "\n\n"))
		_, _ = w.Write([]byte(`event: response.output_text.delta` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","response_id":"resp-test","delta":"lo"}` + "\n\n"))
		_, _ = w.Write([]byte(`event: response.completed` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp-test","object":"response","status":"completed","model":"test-model","output":[{"type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"output_text":"hello"}}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	var events []string
	var payloads []string
	provider := NewOpenAICompatible(server.URL, "", true)
	response, err := provider.StreamResponses(context.Background(), openai.ResponseRequest{
		Model:  "test-model",
		Input:  "hello",
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
		t.Fatal("expected responses upstream stream to be enabled")
	}
	if len(payloads) != 4 {
		t.Fatalf("expected four streamed payloads, got %d: %v", len(payloads), payloads)
	}
	if events[0] != "response.created" || events[1] != "response.output_text.delta" || events[3] != "response.completed" {
		t.Fatalf("unexpected events: %v", events)
	}
	if response.OutputText != "hello" {
		t.Fatalf("expected collected output_text, got %q", response.OutputText)
	}
}
