package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type fallbackChatProvider struct {
	chatProvider
	response openai.ChatCompletionResponse
	calls    int
}

func (p *fallbackChatProvider) ChatCompletions(_ context.Context, request modules.RequestContext) (openai.ChatCompletionResponse, error) {
	p.calls++
	p.request = request
	return p.response, nil
}

func TestChatFallbackStreamPreservesToolsAndUsage(t *testing.T) {
	upstream := &fallbackChatProvider{response: openai.ChatCompletionResponse{
		ID: "chat-fallback", Object: "chat.completion", Model: "test-model",
		Choices: []openai.Choice{{Index: 0, FinishReason: "tool_calls", Message: openai.Message{
			Role: "assistant", Content: "Checking weather",
			ToolCalls: []openai.ToolCall{
				{ID: "call-a", Type: "function", Function: openai.FunctionCall{Name: "weather", Arguments: `{"city":"Paris"}`}, ExtraContent: &openai.ToolCallExtraContent{Google: &openai.GoogleToolCallContent{ThoughtSignature: "opaque-signature"}}},
				{ID: "call-b", Type: "function", Function: openai.FunctionCall{Name: "weather", Arguments: `{"city":"Rome"}`}},
			},
		}}},
		Usage: openai.Usage{PromptTokens: 20, CompletionTokens: 7, TotalTokens: 27, PromptTokensDetails: &openai.PromptTokenDetails{CachedTokens: 5}},
	}}
	handler := Routes(NewHandler(modules.NewPipeline(nil), upstream))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"Weather?"}]}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("response: %d %s", response.Code, response.Body.String())
	}
	if upstream.calls != 1 || upstream.request.Request.Stream {
		t.Fatal("fallback must execute one non-streaming request")
	}
	var calls []openai.ToolCall
	var usage *openai.Usage
	var text, finish string
	var done bool
	usageEvents := 0
	for _, event := range strings.Split(strings.TrimSpace(response.Body.String()), "\n\n") {
		if done {
			t.Fatal("event after DONE")
		}
		data := strings.TrimPrefix(event, "data: ")
		if data == "[DONE]" {
			done = true
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta  openai.Message `json:"delta"`
				Finish string         `json:"finish_reason"`
			} `json:"choices"`
			Usage *openai.Usage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatal(err)
		}
		for _, choice := range chunk.Choices {
			text += openai.ContentText(choice.Delta.Content)
			calls = append(calls, choice.Delta.ToolCalls...)
			if choice.Finish != "" {
				finish = choice.Finish
			}
		}
		if chunk.Usage != nil {
			usageEvents++
			usage = chunk.Usage
			if len(chunk.Choices) != 0 || finish == "" {
				t.Fatal("usage must follow choice completion")
			}
		}
	}
	if !done || finish != "tool_calls" || text != "Checking weather" || len(calls) != 2 {
		t.Fatalf("incomplete stream: done=%v finish=%s text=%s calls=%v", done, finish, text, calls)
	}
	for index, call := range calls {
		if call.Index == nil || *call.Index != index {
			t.Fatalf("tool index: %v", call.Index)
		}
		call.Index = nil
		if !reflect.DeepEqual(call, upstream.response.Choices[0].Message.ToolCalls[index]) {
			t.Fatalf("tool metadata lost: %+v", call)
		}
	}
	if usageEvents != 1 || usage == nil || !reflect.DeepEqual(*usage, upstream.response.Usage) {
		t.Fatalf("usage lost or duplicated: count=%d usage=%+v", usageEvents, usage)
	}
}
