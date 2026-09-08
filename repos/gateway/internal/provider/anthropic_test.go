package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestAnthropicMapsMaxCompletionTokensToMaxTokens(t *testing.T) {
	limit := 321
	request := anthropicChatRequest(openai.ChatCompletionRequest{Model: "claude", MaxCompletionTokens: &limit}, false)
	if request.MaxTokens != limit {
		t.Fatalf("max_completion_tokens was not mapped: %+v", request)
	}
}

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
			Usage: anthropicUsage{InputTokens: 3, OutputTokens: 2, CacheReadInputTokens: 2, CacheCreationInputTokens: 1},
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
	if response.Usage.PromptTokens != 6 || response.Usage.TotalTokens != 8 || response.Usage.PromptTokensDetails == nil || response.Usage.PromptTokensDetails.CachedTokens != 2 || response.Usage.PromptTokensDetails.CacheWriteTokens != 1 {
		t.Fatalf("unexpected usage: %+v", response.Usage)
	}
}

func TestAnthropicConvertsOpenAIVisionContent(t *testing.T) {
	_, messages := anthropicMessages([]openai.Message{{Role: "user", Content: []any{
		map[string]any{"type": "text", "text": "describe"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo="}},
	}}})
	blocks, ok := messages[0].Content.([]anthropicContent)
	if !ok || len(blocks) != 2 || blocks[0].Text != "describe" || blocks[1].Type != "image" {
		t.Fatalf("unexpected Anthropic vision blocks: %+v", messages[0].Content)
	}
	source, ok := blocks[1].Source.(map[string]any)
	if !ok || source["media_type"] != "image/png" || source["data"] != "iVBORw0KGgo=" {
		t.Fatalf("unexpected Anthropic image source: %+v", blocks[1].Source)
	}
}

func TestAnthropicConvertsResponsesVisionInput(t *testing.T) {
	request, err := anthropicResponsesRequest(openai.ResponseRequest{Input: []any{map[string]any{
		"role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": "describe"},
			map[string]any{"type": "input_image", "image_url": "data:image/jpeg;base64,/9j/"},
		},
	}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	blocks, ok := request.Messages[0].Content.([]anthropicContent)
	if !ok || len(blocks) != 2 || blocks[1].Type != "image" {
		t.Fatalf("unexpected Responses vision conversion: %+v", request.Messages)
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
		_, _ = w.Write([]byte(`data: {"type":"message_start","message":{"id":"msg-test","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":3,"cache_read_input_tokens":2,"cache_creation_input_tokens":1}}}` + "\n\n"))
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
	if openai.ContentText(response.Choices[0].Message.Content) != "hello" || response.Usage.PromptTokens != 6 || response.Usage.TotalTokens != 8 || response.Usage.PromptTokensDetails == nil || response.Usage.PromptTokensDetails.CachedTokens != 2 || response.Usage.PromptTokensDetails.CacheWriteTokens != 1 {
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
			Usage: anthropicUsage{InputTokens: 4, OutputTokens: 1, CacheReadInputTokens: 3, CacheCreationInputTokens: 2},
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
	if response.Usage.InputTokens != 9 || response.Usage.TotalTokens != 10 || response.Usage.InputTokensDetails == nil || response.Usage.InputTokensDetails.CachedTokens != 3 || response.Usage.InputTokensDetails.CacheWriteTokens != 2 {
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
		_, _ = w.Write([]byte(`data: {"type":"message_start","message":{"id":"msg-test","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":4,"cache_read_input_tokens":3,"cache_creation_input_tokens":2}}}` + "\n\n"))
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
	if response.OutputText != "pong" || response.Usage.InputTokens != 9 || response.Usage.TotalTokens != 10 || response.Usage.InputTokensDetails == nil || response.Usage.InputTokensDetails.CachedTokens != 3 || response.Usage.InputTokensDetails.CacheWriteTokens != 2 {
		t.Fatalf("unexpected streamed output_text: %s", response.OutputText)
	}
}

func TestAnthropicTranslatesToolCalls(t *testing.T) {
	var upstream anthropicRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(anthropicResponse{
			ID: "msg-tool", Model: upstream.Model, StopReason: "tool_use",
			Content: []anthropicContent{{Type: "tool_use", ID: "toolu-1", Name: "weather", Input: map[string]any{"city": "Moscow"}}},
		})
	}))
	defer server.Close()

	response, err := NewAnthropic(server.URL, "key", false).ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model: "claude-test", Messages: []openai.Message{{Role: "user", Content: "weather"}},
		Tools:      []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "weather", Description: "Get weather", Parameters: map[string]any{"type": "object"}}}},
		ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "weather"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(upstream.Tools) != 1 || upstream.Tools[0].Name != "weather" || upstream.ToolChoice["type"] != "tool" || upstream.ToolChoice["name"] != "weather" {
		t.Fatalf("unexpected anthropic tool request: %+v", upstream)
	}
	call := response.Choices[0].Message.ToolCalls[0]
	if response.Choices[0].FinishReason != "tool_calls" || call.ID != "toolu-1" || call.Function.Arguments != `{"city":"Moscow"}` {
		t.Fatalf("unexpected translated tool call: %+v", response)
	}
}

func TestAnthropicEmulatesStructuredOutputWithForcedTool(t *testing.T) {
	var upstream anthropicRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(anthropicResponse{
			ID: "msg-json", Model: upstream.Model, StopReason: "tool_use",
			Content: []anthropicContent{{Type: "tool_use", ID: "toolu-json", Name: "answer", Input: map[string]any{"ok": true}}},
		})
	}))
	defer server.Close()

	strict := true
	response, err := NewAnthropic(server.URL, "key", false).ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model: "claude-test", Messages: []openai.Message{{Role: "user", Content: "return json"}},
		ResponseFormat: &openai.ResponseFormat{Type: "json_schema", JSONSchema: &openai.JSONSchemaFormat{Name: "answer", Schema: map[string]any{"type": "object"}, Strict: &strict}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if upstream.ToolChoice["name"] != "answer" || len(upstream.Tools) != 1 {
		t.Fatalf("structured output tool was not forced: %+v", upstream)
	}
	if openai.ContentText(response.Choices[0].Message.Content) != `{"ok":true}` || len(response.Choices[0].Message.ToolCalls) != 0 {
		t.Fatalf("structured response was not normalized: %+v", response)
	}
}

func TestAnthropicStreamsToolCallArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_start","message":{"id":"msg-tool","model":"claude-test","usage":{"input_tokens":3}}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_start\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu-1","name":"weather","input":{}}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"Moscow\"}"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_stop"}` + "\n\n"))
	}))
	defer server.Close()

	var payloads []string
	response, err := NewAnthropic(server.URL, "key", true).StreamChatCompletions(context.Background(), openai.ChatCompletionRequest{Model: "claude-test"}, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	call := response.Choices[0].Message.ToolCalls[0]
	if call.ID != "toolu-1" || call.Function.Name != "weather" || call.Function.Arguments != `{"city":"Moscow"}` {
		t.Fatalf("unexpected streamed tool call: %+v", call)
	}
	if len(payloads) != 4 || !strings.Contains(payloads[0], `"tool_calls"`) || !strings.Contains(payloads[3], `"finish_reason":"tool_calls"`) {
		t.Fatalf("unexpected OpenAI tool stream: %v", payloads)
	}
}

func TestAnthropicMapsStopAndParallelControls(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("parallel=%v/stream=%v", parallel, streaming), func(t *testing.T) {
				var upstream anthropicRequest
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
						t.Error(err)
						return
					}
					if streaming {
						_, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"test\",\"model\":\"test\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
					} else {
						_, _ = fmt.Fprint(w, `{"id":"test","content":[],"stop_reason":"stop_sequence","stop_sequence":"END"}`)
					}
				}))
				defer server.Close()
				client := NewAnthropic(server.URL, "", true)
				request := openai.ChatCompletionRequest{Model: "test", Stop: []any{"\n", "END"}, ParallelToolCalls: &parallel, Tools: []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "lookup"}}}, ToolChoice: "required"}
				var err error
				if streaming {
					_, err = client.StreamChatCompletions(context.Background(), request, func(string) error { return nil })
				} else {
					_, err = client.ChatCompletions(context.Background(), request)
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(upstream.StopSequences) != 2 || upstream.StopSequences[0] != "\n" || upstream.StopSequences[1] != "END" || upstream.ToolChoice["disable_parallel_tool_use"] != !parallel || upstream.ToolChoice["type"] != "any" {
					t.Fatalf("native controls lost: %+v", upstream)
				}
				responseRequest := openai.ResponseRequest{Model: "test", ParallelToolCalls: &parallel, Tools: []openai.ResponseTool{{Type: "function", Name: "lookup"}}, ToolChoice: "required"}
				if streaming {
					_, err = client.StreamResponses(context.Background(), responseRequest, func(string, string) error { return nil })
				} else {
					_, err = client.Responses(context.Background(), responseRequest)
				}
				if err != nil {
					t.Fatal(err)
				}
				if upstream.ToolChoice["disable_parallel_tool_use"] != !parallel {
					t.Fatalf("Responses parallel control lost: %+v", upstream)
				}
			})
		}
	}
}

func TestAnthropicParallelControlWithoutTools(t *testing.T) {
	parallel := false
	request := anthropicChatRequest(openai.ChatCompletionRequest{ParallelToolCalls: &parallel}, false)
	if request.ToolChoice != nil {
		t.Fatal("parallel control invented tool choice")
	}
	request = anthropicChatRequest(openai.ChatCompletionRequest{ParallelToolCalls: &parallel, ResponseFormat: &openai.ResponseFormat{Type: "json_schema", JSONSchema: &openai.JSONSchemaFormat{Name: "answer", Schema: map[string]any{"type": "object"}}}}, false)
	if request.ToolChoice["disable_parallel_tool_use"] != true {
		t.Fatal("structured output lost parallel control")
	}
}
