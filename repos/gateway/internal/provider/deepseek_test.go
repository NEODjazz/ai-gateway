package provider

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestDeepSeekChatMapsSupportedContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer deepseek-key" {
			t.Fatalf("path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		thinking, _ := body["thinking"].(map[string]any)
		if body["model"] != "model" || body["max_tokens"] != float64(64) || body["max_completion_tokens"] != nil || body["user"] != nil || body["user_id"] != "tenant_1" || thinking["type"] != "disabled" {
			t.Fatalf("request=%#v", body)
		}
		if body["logprobs"] != true || body["top_logprobs"] != float64(4) || len(body["stop"].([]any)) != 5 || len(body["tools"].([]any)) != 1 || body["response_format"] == nil || body["reasoning_effort"] != nil {
			t.Fatalf("request=%#v", body)
		}
		_, _ = fmt.Fprint(w, `{"id":"chat","object":"chat.completion","model":"model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_cache_hit_tokens":7}}`)
	}))
	defer server.Close()
	maxTokens, topLogprobs, logprobs, n := 64, 4, true, 1
	client := NewDeepSeek(server.URL, "deepseek-key", true)
	response, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, MaxCompletionTokens: &maxTokens, Stop: []string{"1", "2", "3", "4", "5"},
		ChatGenerationOptions: openai.ChatGenerationOptions{User: "tenant_1", Logprobs: &logprobs, TopLogprobs: &topLogprobs, N: &n},
		Tools:                 []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "lookup", Parameters: map[string]any{"type": "object"}}}},
		ResponseFormat:        &openai.ResponseFormat{Type: "json_object"},
	})
	if err != nil || response.Usage.PromptTokensDetails == nil || response.Usage.PromptTokensDetails.CachedTokens != 7 || openai.ContentText(response.Choices[0].Message.Content) != "ok" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestDeepSeekChatMapsThinkingControls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		thinking, _ := body["thinking"].(map[string]any)
		if thinking["type"] != "enabled" || body["reasoning_effort"] != "high" || body["top_p"] != 0.97 {
			t.Fatalf("request=%#v", body)
		}
		_, _ = fmt.Fprint(w, `{"id":"chat","object":"chat.completion","model":"deepseek-flash","choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"plan","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)
	}))
	defer server.Close()
	topP := 0.97
	response, err := NewDeepSeek(server.URL, "key", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "deepseek-flash", Messages: []openai.Message{{Role: "user", Content: "hello"}}, TopP: &topP,
		ChatGenerationOptions: openai.ChatGenerationOptions{Thinking: &openai.ChatThinkingOptions{Type: "enabled"}, ReasoningEffort: "high"},
	})
	if err != nil || response.Choices[0].Message.ReasoningContent != "plan" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestDeepSeekRejectsInvalidThinkingCombinationsBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	temperature, lowTopP := 0.5, 0.5
	requests := []openai.ChatCompletionRequest{
		{TopP: &lowTopP},
		{ChatGenerationOptions: openai.ChatGenerationOptions{Thinking: &openai.ChatThinkingOptions{Type: "disabled"}}, TopP: &lowTopP},
		{ChatGenerationOptions: openai.ChatGenerationOptions{Thinking: &openai.ChatThinkingOptions{Type: "disabled"}, ReasoningEffort: "high"}},
		{ChatGenerationOptions: openai.ChatGenerationOptions{Thinking: &openai.ChatThinkingOptions{Type: "enabled"}, ReasoningEffort: "none"}},
		{ChatGenerationOptions: openai.ChatGenerationOptions{Thinking: &openai.ChatThinkingOptions{Type: "enabled"}}, Temperature: &temperature},
		{ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: "high"}, TopP: &lowTopP},
		{ChatGenerationOptions: openai.ChatGenerationOptions{Thinking: &openai.ChatThinkingOptions{Type: "enabled"}}, ToolChoice: "required"},
		{ChatGenerationOptions: openai.ChatGenerationOptions{Thinking: &openai.ChatThinkingOptions{Type: "enabled"}}, ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}}},
	}
	for _, request := range requests {
		request.Model = "deepseek-flash"
		request.Messages = []openai.Message{{Role: "user", Content: "hello"}}
		if _, err := NewDeepSeek(server.URL, "key", false).ChatCompletions(t.Context(), request); err == nil || called {
			t.Fatalf("request=%+v err=%v called=%v", request, err, called)
		}
	}
}

func TestDeepSeekResponsesMapsSupportedContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer key" {
			t.Fatalf("path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		reasoning, _ := body["reasoning"].(map[string]any)
		text, _ := body["text"].(map[string]any)
		format, _ := text["format"].(map[string]any)
		if body["max_output_tokens"] != float64(80) || body["user"] != "tenant_2" || reasoning["effort"] != "high" || format["type"] != "json_schema" || len(body["tools"].([]any)) != 1 {
			t.Fatalf("request=%#v", body)
		}
		_, _ = fmt.Fprint(w, `{"id":"resp","object":"response","created_at":1,"status":"completed","model":"model","output":[{"id":"msg","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4,"input_tokens_details":{"cached_tokens":2}}}`)
	}))
	defer server.Close()
	limit, effort := 80, "high"
	client := NewDeepSeek(server.URL, "key", true)
	response, err := client.Responses(t.Context(), openai.ResponseRequest{
		Model: "model", Input: "hello", User: "tenant_2", MaxOutputTokens: &limit, Reasoning: &openai.ResponseReasoning{Effort: &effort},
		Tools: []openai.ResponseTool{{Type: "function", Name: "lookup", Parameters: map[string]any{"type": "object"}}},
		Text:  map[string]any{"format": map[string]any{"type": "json_schema", "name": "answer", "schema": map[string]any{"type": "object"}}},
	})
	if err != nil || response.OutputText != "ok" || response.Usage.InputTokensDetails == nil || response.Usage.InputTokensDetails.CachedTokens != 2 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestDeepSeekResponsesReasoningEffortWire(t *testing.T) {
	for _, effort := range []string{"none", "minimal"} {
		t.Run(effort, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				reasoning, _ := body["reasoning"].(map[string]any)
				if reasoning["effort"] != effort {
					t.Fatalf("reasoning=%v", reasoning)
				}
				_, _ = fmt.Fprint(w, `{"id":"resp","object":"response","created_at":1,"status":"completed","model":"deepseek-flash","output":[{"id":"msg","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
			}))
			defer server.Close()
			response, err := NewDeepSeek(server.URL, "key", false).Responses(t.Context(), openai.ResponseRequest{
				Model: "deepseek-flash", Input: "hello", Reasoning: &openai.ResponseReasoning{Effort: &effort},
			})
			if err != nil || response.OutputText != "ok" {
				t.Fatalf("response=%+v err=%v", response, err)
			}
		})
	}
}

func TestDeepSeekResponsesRejectsIgnoredInputItemTypes(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewDeepSeek(server.URL, "key", false)
	for _, input := range []any{
		[]any{map[string]any{"type": "file_search_call", "id": "search_1"}},
		[]any{map[string]any{"type": "unrecognized", "content": "ignored"}},
		[]any{map[string]any{"content": "missing role"}},
		[]any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_file", "file_id": "file_1"}}}},
		[]any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "unknown", "text": "ignored"}}}},
		[]any{map[string]any{"type": "function_call_output", "call_id": "call_1", "output": []any{map[string]any{"type": "input_file", "file_id": "file_1"}}}},
		[]any{map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "ignored"}}}},
		[]any{map[string]any{"type": "reasoning", "encrypted_content": "opaque"}},
		[]any{map[string]any{"type": "reasoning", "content": []any{map[string]any{"type": "summary_text", "text": "ignored"}}}},
	} {
		_, err := client.Responses(t.Context(), openai.ResponseRequest{Model: "deepseek-flash", Input: input})
		var failure *Error
		if !errors.As(err, &failure) || failure.Param != "input" || failure.UpstreamCode != "unsupported_parameter" || called {
			t.Fatalf("input=%v err=%v called=%v", input, err, called)
		}
	}
}

func TestDeepSeekResponsesAcceptsDocumentedInputItemTypes(t *testing.T) {
	for _, item := range []map[string]any{
		{"role": "user", "content": "hello"},
		{"type": "message", "role": "user", "content": "hello"},
		{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hello"}, map[string]any{"type": "input_image", "image_url": "https://example.test/image.png"}}},
		{"type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": "{}"},
		{"type": "function_call_output", "call_id": "call_1", "output": "ok"},
		{"type": "function_call_output", "call_id": "call_1", "output": []any{map[string]any{"type": "output_text", "text": "ok"}}},
		{"type": "custom_tool_call", "call_id": "call_1", "name": "apply_patch", "input": "patch"},
		{"type": "custom_tool_call_output", "call_id": "call_1", "output": "ok"},
		{"type": "reasoning", "content": []any{map[string]any{"type": "reasoning_text", "text": "plan"}}},
		{"type": "web_search_call", "id": "search_1"},
	} {
		if err := validateDeepSeekResponseInput([]any{item}, "deepseek-flash"); err != nil {
			t.Fatalf("item=%v err=%v", item, err)
		}
	}
}

func TestDeepSeekVisionIsScopedToFlashModels(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewDeepSeek(server.URL, "key", false)
	imageURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n"))
	chat := openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user", Content: []any{
		map[string]any{"type": "text", "text": "describe"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": imageURL}},
	}}}}
	for _, model := range []string{"deepseek-flash", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp"} {
		chat.Model = model
		if err := client.ValidateChatParameters(chat); err != nil {
			t.Fatalf("model=%s chat err=%v", model, err)
		}
	}
	chat.Model = "deepseek-v4-pro"
	if _, err := client.ChatCompletions(t.Context(), chat); err == nil || called {
		t.Fatalf("pro chat err=%v called=%v", err, called)
	}
	for _, input := range []any{
		[]any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": imageURL}}}},
		[]any{map[string]any{"type": "function_call_output", "call_id": "call_1", "output": []any{map[string]any{"type": "input_image", "image_url": imageURL}}}},
	} {
		if err := validateDeepSeekResponseInput(input, "deepseek-flash"); err != nil {
			t.Fatalf("flash input=%v err=%v", input, err)
		}
		_, err := client.Responses(t.Context(), openai.ResponseRequest{Model: "deepseek-v4-pro", Input: input})
		var failure *Error
		if !errors.As(err, &failure) || failure.Param != "input" || called {
			t.Fatalf("pro input=%v err=%v called=%v", input, err, called)
		}
	}
}

func TestDeepSeekStreamsResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["stream"] != true {
			t.Fatalf("request=%#v", body)
		}
		_, _ = fmt.Fprint(w, responseTestTerminal)
	}))
	defer server.Close()
	client := NewDeepSeek(server.URL, "key", true)
	var events int
	response, err := client.StreamResponses(t.Context(), openai.ResponseRequest{Model: "model", Input: "hello", Stream: true}, func(string, string) error {
		events++
		return nil
	})
	if err != nil || response.Status != "completed" || events != 1 {
		t.Fatalf("response=%+v events=%d err=%v", response, events, err)
	}
}

func TestDeepSeekRejectsUnsupportedParametersBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewDeepSeek(server.URL, "key", true)
	penalty := 0.5
	_, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{FrequencyPenalty: &penalty}})
	if err == nil || !strings.Contains(err.Error(), "frequency_penalty") || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
	effort, summary := "high", "auto"
	_, err = client.Responses(t.Context(), openai.ResponseRequest{Model: "model", Input: "hello", Reasoning: &openai.ResponseReasoning{Effort: &effort, Summary: &summary}})
	if err == nil || !strings.Contains(err.Error(), "reasoning") || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
	_, err = client.Responses(t.Context(), openai.ResponseRequest{Model: "model", Input: "hello", PreviousResponse: "resp_previous"})
	if err == nil || !strings.Contains(err.Error(), "previous_response_id") || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
	_, err = client.Responses(t.Context(), openai.ResponseRequest{Model: "model", Input: "hello", User: "contains space"})
	if err == nil || !strings.Contains(err.Error(), "user") || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestDeepSeekRejectsInvalidStopAndUser(t *testing.T) {
	client := NewDeepSeek("http://unused.invalid", "key", true)
	for _, request := range []openai.ChatCompletionRequest{
		{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, Stop: make([]string, 17)},
		{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{User: "contains space"}},
	} {
		if _, err := client.ChatCompletions(t.Context(), request); err == nil {
			t.Fatalf("request was accepted: %+v", request)
		}
	}
}
