package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestCohereChatV2ProtocolAndUsage(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/proxy/v2/chat" || r.Header.Get("Authorization") != "Bearer provider-key" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("unexpected request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		var request cohereChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "command" || len(request.Messages) != 3 || request.Messages[1].Role != "system" || request.Messages[2].Content != "hello" || request.MaxTokens == nil || *request.MaxTokens != 7 || request.P == nil || *request.P != 0.8 || request.K == nil || *request.K != 40 || request.Seed == nil || *request.Seed != 42 || request.FrequencyPenalty == nil || *request.FrequencyPenalty != 0.2 || request.PresencePenalty == nil || *request.PresencePenalty != 0.3 || request.Logprobs == nil || !*request.Logprobs || len(request.StopSequences) != 1 || request.ResponseFormat == nil || request.ResponseFormat.Type != "json_object" {
			t.Fatalf("request fields lost: %+v", request)
		}
		schema, _ := request.ResponseFormat.Schema.(map[string]any)
		if schema["type"] != "object" {
			t.Fatalf("schema lost: %+v", request.ResponseFormat.Schema)
		}
		_, _ = fmt.Fprint(w, `{"id":"chat-1","finish_reason":"COMPLETE","message":{"role":"assistant","content":[{"type":"text","text":"{\"ok\":"},{"type":"text","text":"true}"}]},"usage":{"billed_units":{"input_tokens":3,"output_tokens":2},"tokens":{"input_tokens":30,"output_tokens":20}},"logprobs":[{"text":"{\"ok\":","token_ids":[1],"logprobs":[-0.1]},{"text":"true}","token_ids":[2],"logprobs":[-0.2]}]}`)
	}))
	defer server.Close()

	maxTokens, topP, topK, frequencyPenalty, presencePenalty := 7, 0.8, 40, 0.2, 0.3
	seed := int64(42)
	logprobs := true
	response, err := NewCohere(server.URL+"/proxy/v1", "provider-key").ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model: "command", Messages: []openai.Message{{Role: "system", Content: "first"}, {Role: "developer", Content: "second"}, {Role: "user", Content: "hello"}},
		ResponseFormat:      &openai.ResponseFormat{Type: "json_schema", JSONSchema: &openai.JSONSchemaFormat{Name: "answer", Schema: map[string]any{"type": "object"}}},
		MaxCompletionTokens: &maxTokens, TopP: &topP, Seed: &seed, Stop: []string{"done"}, Stream: true, StreamOptions: &openai.ChatStreamOptions{IncludeUsage: true},
		ChatGenerationOptions: openai.ChatGenerationOptions{TopK: &topK, FrequencyPenalty: &frequencyPenalty, PresencePenalty: &presencePenalty, Logprobs: &logprobs},
	})
	if err != nil || calls.Load() != 1 || response.ID != "chat-1" || response.Choices[0].Message.Content != `{"ok":true}` || response.Choices[0].FinishReason != "stop" || response.Choices[0].Logprobs == nil || len(response.Choices[0].Logprobs.Content) != 2 || response.Choices[0].Logprobs.Content[0].Token != `{"ok":` || response.Usage.PromptTokens != 3 || response.Usage.CompletionTokens != 2 || response.Usage.TotalTokens != 5 {
		t.Fatalf("response=%+v calls=%d err=%v", response, calls.Load(), err)
	}
}

func TestCohereChatRejectsUnsupportedParametersBeforeUpstream(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	max, completionMax := 1, 2
	invalidTemperature, invalidTopP := 1.1, 0.0
	invalidTopK := 501
	negativeSeed := int64(-1)
	invalidPenalty := 1.1
	requests := []struct {
		param string
		value openai.ChatCompletionRequest
	}{
		{param: "messages", value: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "tool", Content: "result", ToolCallID: "call"}}}},
		{param: "messages", value: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user", Content: []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo="}}}}}}},
		{param: "max_tokens", value: openai.ChatCompletionRequest{MaxTokens: &max, MaxCompletionTokens: &completionMax}},
		{param: "max_tokens", value: openai.ChatCompletionRequest{MaxTokens: new(int)}},
		{param: "temperature", value: openai.ChatCompletionRequest{Temperature: &invalidTemperature}},
		{param: "top_p", value: openai.ChatCompletionRequest{TopP: &invalidTopP}},
		{param: "top_k", value: openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{TopK: &invalidTopK}}},
		{param: "seed", value: openai.ChatCompletionRequest{Seed: &negativeSeed}},
		{param: "frequency_penalty", value: openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{FrequencyPenalty: &invalidPenalty}}},
		{param: "presence_penalty", value: openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{PresencePenalty: &invalidPenalty}}},
		{param: "stop", value: openai.ChatCompletionRequest{Stop: []string{"1", "2", "3", "4", "5", "6"}}},
		{param: "messages", value: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user", Content: []any{map[string]any{"type": "input_text", "text": "hello"}}}}}},
		{param: "messages", value: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "unknown", Content: "hello"}}}},
		{param: "response_format", value: openai.ChatCompletionRequest{ResponseFormat: &openai.ResponseFormat{Type: "json_schema"}}},
	}
	for _, test := range requests {
		test.value.Model = "command"
		if len(test.value.Messages) == 0 {
			test.value.Messages = []openai.Message{{Role: "user", Content: "hello"}}
		}
		_, err := NewCohere(server.URL, "key").ChatCompletions(context.Background(), test.value)
		var failure *Error
		if !errors.As(err, &failure) || failure.Param != test.param {
			t.Fatalf("param=%s error=%v", test.param, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unsupported chat request reached upstream")
	}
}

func TestCohereChatRejectsMalformedResponses(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"id":"x","finish_reason":"ERROR","message":{"role":"assistant","content":[{"type":"text","text":"x"}]},"usage":{"tokens":{"input_tokens":1,"output_tokens":1}}}`,
		`{"id":"x","finish_reason":"COMPLETE","message":{"role":"assistant","content":[{"type":"citation","text":"x"}]},"usage":{"tokens":{"input_tokens":1,"output_tokens":1}}}`,
		`{"id":"x","finish_reason":"COMPLETE","message":{"role":"assistant","content":[{"type":"text","text":"x"}]}}`,
		`{"id":"x","finish_reason":"COMPLETE","message":{"role":"assistant","content":[{"type":"text","text":"x"}]},"usage":{"billed_units":{"input_tokens":-1,"output_tokens":1}}}`,
		`{"id":"x","finish_reason":"COMPLETE","message":{"role":"assistant","content":[{"type":"text","text":"x"}]},"usage":{"billed_units":{"input_tokens":1},"tokens":{"input_tokens":10,"output_tokens":10}}}`,
		`{"id":"x","finish_reason":"TOOL_CALL","message":{"role":"assistant","content":[{"type":"text","text":"x"}]},"usage":{"tokens":{"input_tokens":1,"output_tokens":1}}}`,
		`{"id":"x","finish_reason":"COMPLETE","message":{"role":"assistant","content":[{"type":"text","text":"x"}]},"usage":{"tokens":{"input_tokens":1,"output_tokens":1}}} {}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, body) }))
		_, err := NewCohere(server.URL, "").ChatCompletions(context.Background(), openai.ChatCompletionRequest{Model: "command", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
		server.Close()
		if err == nil {
			t.Fatalf("invalid response accepted: %s", body)
		}
	}
}

func TestCohereChatRejectsMissingRequestedLogprobs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"x","finish_reason":"COMPLETE","message":{"role":"assistant","content":[{"type":"text","text":"x"}]},"usage":{"tokens":{"input_tokens":1,"output_tokens":1}}}`)
	}))
	defer server.Close()
	enabled := true
	_, err := NewCohere(server.URL, "").ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model: "command", Messages: []openai.Message{{Role: "user", Content: "hello"}},
		ChatGenerationOptions: openai.ChatGenerationOptions{Logprobs: &enabled},
	})
	if err == nil || !strings.Contains(err.Error(), "omitted requested logprobs") {
		t.Fatalf("missing requested logprobs accepted: %v", err)
	}
}

func TestCohereRejectsUnrepresentableLogprobItems(t *testing.T) {
	text := "x"
	validProbability := []float64{-0.1}
	multipleProbabilities := []float64{-0.1, -0.2}
	positiveProbability := []float64{0.1}
	for name, item := range map[string]cohereLogprobItem{
		"missing text":           {TokenIDs: []int{1}, Logprobs: &validProbability},
		"missing token id":       {Text: &text, Logprobs: &validProbability},
		"negative token id":      {Text: &text, TokenIDs: []int{-1}, Logprobs: &validProbability},
		"multiple token ids":     {Text: &text, TokenIDs: []int{1, 2}, Logprobs: &validProbability},
		"missing probabilities":  {Text: &text, TokenIDs: []int{1}},
		"multiple probabilities": {Text: &text, TokenIDs: []int{1}, Logprobs: &multipleProbabilities},
		"positive probability":   {Text: &text, TokenIDs: []int{1}, Logprobs: &positiveProbability},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := cohereChoiceLogprobs([]cohereLogprobItem{item}); err == nil {
				t.Fatal("unrepresentable logprob item accepted")
			}
		})
	}
}

func TestCohereChatMapsFinishReasons(t *testing.T) {
	for upstream, expected := range map[string]string{"COMPLETE": "stop", "STOP_SEQUENCE": "stop", "MAX_TOKENS": "length"} {
		actual, err := cohereFinishReason(upstream)
		if err != nil || actual != expected {
			t.Fatalf("%s => %s, %v", upstream, actual, err)
		}
	}
	for _, value := range []string{"timeout", "TOOL_CALL"} {
		if _, err := cohereFinishReason(value); err == nil || !strings.Contains(err.Error(), "finish reason") {
			t.Fatalf("terminal provider finish reason %q accepted", value)
		}
	}
}
