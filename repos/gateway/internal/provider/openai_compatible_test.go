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

func TestOpenAICompatibleEmbeddings(t *testing.T) {
	var upstream openAICompatibleEmbeddingRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer provider-key" {
			t.Fatalf("missing provider authorization")
		}
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(openai.EmbeddingResponse{Object: "list", Model: "embed-model", Data: []openai.Embedding{{Object: "embedding", Embedding: []float64{0.1, 0.2}, Index: 0}}, Usage: openai.Usage{PromptTokens: 2, TotalTokens: 2}})
	}))
	defer server.Close()
	dimensions := 2
	response, err := NewOpenAICompatible(server.URL, "provider-key", false).Embeddings(context.Background(), openai.EmbeddingRequest{Model: "embed-model", Input: []any{"hello"}, EncodingFormat: "float", Dimensions: &dimensions, User: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	if upstream.Model != "embed-model" || upstream.Dimensions == nil || *upstream.Dimensions != 2 || upstream.User != "user-1" {
		t.Fatalf("embedding request was not forwarded: %+v", upstream)
	}
	if len(response.Data) != 1 || len(response.Data[0].Embedding) != 2 || response.Usage.TotalTokens != 2 {
		t.Fatalf("unexpected embedding response: %+v", response)
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
		Model: "test-model", Input: "hello", Stream: true, PreviousResponse: "resp-previous",
		Tools: []openai.ResponseTool{
			{Type: "function", Name: "weather", Parameters: map[string]any{"type": "object"}},
			{Type: "mcp", ServerLabel: "weather-prod", ServerURL: "https://mcp.example.test", AllowedTools: []string{"forecast"}, RequireApproval: "never", Headers: map[string]string{"X-MCP-Key": "scoped"}},
		},
		ToolChoice: "auto", Text: map[string]any{"format": map[string]any{"type": "json_object"}},
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
	if upstreamRequest.PreviousResponse != "resp-previous" || len(upstreamRequest.Tools) != 2 || upstreamRequest.Tools[0].Name != "weather" || upstreamRequest.Tools[1].ServerLabel != "weather-prod" || upstreamRequest.Tools[1].Headers["X-MCP-Key"] != "scoped" || upstreamRequest.Text == nil {
		t.Fatalf("responses tools/state/format were not forwarded: %+v", upstreamRequest)
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

func TestOpenAICompatibleForwardsToolsAndStructuredOutput(t *testing.T) {
	var upstream openAICompatibleChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
			ID: "chat-tools", Object: "chat.completion", Model: upstream.Model,
			Choices: []openai.Choice{{Index: 0, FinishReason: "tool_calls", Message: openai.Message{
				Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "call-1", Type: "function", Function: openai.FunctionCall{Name: "weather", Arguments: `{"city":"Moscow"}`}}},
			}}},
		})
	}))
	defer server.Close()

	strict := true
	response, err := NewOpenAICompatible(server.URL, "", false).ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model: "test-model", Messages: []openai.Message{{Role: "user", Content: "weather"}},
		Tools:      []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "weather", Parameters: map[string]any{"type": "object"}}}},
		ToolChoice: "required", ParallelToolCalls: &strict,
		ResponseFormat: &openai.ResponseFormat{Type: "json_schema", JSONSchema: &openai.JSONSchemaFormat{Name: "weather_result", Schema: map[string]any{"type": "object"}, Strict: &strict}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(upstream.Tools) != 1 || upstream.Tools[0].Function.Name != "weather" || upstream.ToolChoice != "required" || upstream.ResponseFormat == nil {
		t.Fatalf("tool contract was not forwarded: %+v", upstream)
	}
	if len(response.Choices[0].Message.ToolCalls) != 1 || response.Choices[0].Message.ToolCalls[0].Function.Arguments != `{"city":"Moscow"}` {
		t.Fatalf("tool response was not preserved: %+v", response)
	}
}

func TestOpenAICompatibleCollectsStreamingToolCallArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chat-tools\",\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"weather\",\"arguments\":\"{\\\"city\\\":\"}}]},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chat-tools\",\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"Moscow\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	response, err := NewOpenAICompatible(server.URL, "", true).StreamChatCompletions(context.Background(), openai.ChatCompletionRequest{Model: "test-model"}, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	call := response.Choices[0].Message.ToolCalls[0]
	if call.ID != "call-1" || call.Function.Name != "weather" || call.Function.Arguments != `{"city":"Moscow"}` {
		t.Fatalf("unexpected accumulated tool call: %+v", call)
	}
}
