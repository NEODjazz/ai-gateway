package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestOllamaMapsMaxCompletionTokensToNumPredict(t *testing.T) {
	limit := 321
	options := ollamaRequestOptions(openai.ChatCompletionRequest{MaxCompletionTokens: &limit})
	if options.NumPredict == nil || *options.NumPredict != limit {
		t.Fatalf("max_completion_tokens was not mapped: %+v", options)
	}
}

func TestOllamaCompletionsUsesProviderContract(t *testing.T) {
	var upstream openAICompatibleCompletionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"id":"cmpl-ollama","object":"text_completion","created":7,"model":"phi3","choices":[{"index":0,"text":"done","finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
	}))
	defer server.Close()
	maxTokens := 12
	response, err := NewOllama(server.URL, false).Completions(t.Context(), openai.CompletionRequest{Model: "phi3", Prompt: "complete", MaxTokens: &maxTokens})
	if err != nil {
		t.Fatal(err)
	}
	if upstream.Prompt != "complete" || upstream.MaxTokens == nil || *upstream.MaxTokens != 12 || upstream.Stream {
		t.Fatalf("unexpected upstream request: %+v", upstream)
	}
	if response.Choices[0].Text != "done" || response.Usage.TotalTokens != 4 {
		t.Fatalf("unexpected completion response: %+v", response)
	}
}

func TestOllamaStreamsProviderCompletions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var upstream openAICompatibleCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		if !upstream.Stream {
			t.Fatal("native completion stream was not requested")
		}
		if upstream.StreamOptions == nil || !upstream.StreamOptions.IncludeUsage {
			t.Fatal("completion stream usage was not requested")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"cmpl-ollama\",\"object\":\"text_completion\",\"created\":7,\"model\":\"phi3\",\"choices\":[{\"index\":0,\"text\":\"hello\",\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	var payloads []string
	response, err := NewOllama(server.URL, true).StreamCompletions(t.Context(), openai.CompletionRequest{Model: "phi3", Prompt: "complete", Stream: true}, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 2 || response.Choices[0].Text != "hello" || response.Usage.TotalTokens != 4 {
		t.Fatalf("unexpected completion stream: payloads=%v response=%+v", payloads, response)
	}
}

func TestOllamaChatCompletions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		var request ollamaChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "test-model" {
			t.Fatalf("unexpected model: %s", request.Model)
		}
		if request.Stream {
			t.Fatal("expected non-stream request")
		}

		_ = json.NewEncoder(w).Encode(ollamaChatResponse{
			Model: "test-model",
			Message: openai.Message{
				Role:    "assistant",
				Content: "hello",
			},
			DoneReason:      "stop",
			PromptEvalCount: 4,
			EvalCount:       2,
		})
	}))
	defer server.Close()

	provider := NewOllama(server.URL, false)
	response, err := provider.ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model: "test-model",
		Messages: []openai.Message{
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if response.Model != "test-model" {
		t.Fatalf("unexpected response model: %s", response.Model)
	}
	if openai.ContentText(response.Choices[0].Message.Content) != "hello" {
		t.Fatalf("unexpected content: %s", openai.ContentText(response.Choices[0].Message.Content))
	}
	if response.Usage.TotalTokens != 6 {
		t.Fatalf("unexpected total tokens: %d", response.Usage.TotalTokens)
	}
}

func TestOllamaNormalizesToolArgumentsAndForwardsOptions(t *testing.T) {
	var upstream ollamaChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"model":"llama3.2:latest","message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","function":{"name":"weather.get","arguments":{"city":"Moscow"}}}]},"done":true,"done_reason":"stop"}`))
	}))
	defer server.Close()

	maxTokens := 32
	temperature := 0.0
	topP := 0.7
	topK := 40
	minP := 0.05
	seed := int64(42)
	response, err := NewOllama(server.URL, false).ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model: "llama3.2:latest",
		Messages: []openai.Message{{
			Role: "assistant", ToolCalls: []openai.ToolCall{{
				ID: "previous", Type: "function",
				Function: openai.FunctionCall{Name: "weather.get", Arguments: `{"city":"Kazan"}`},
			}},
		}},
		MaxTokens: &maxTokens, Temperature: &temperature, TopP: &topP, Seed: &seed,
		ChatGenerationOptions: openai.ChatGenerationOptions{TopK: &topK, MinP: &minP},
		Stop:                  []string{"END"},
	})
	if err != nil {
		t.Fatal(err)
	}
	arguments, ok := upstream.Messages[0].ToolCalls[0].Function.Arguments.(map[string]any)
	if !ok || arguments["city"] != "Kazan" {
		t.Fatalf("tool arguments were not converted to an Ollama object: %#v", upstream.Messages[0].ToolCalls[0].Function.Arguments)
	}
	if upstream.Options.NumPredict == nil || *upstream.Options.NumPredict != 32 ||
		upstream.Options.Temperature == nil || *upstream.Options.Temperature != 0 ||
		upstream.Options.TopP == nil || *upstream.Options.TopP != 0.7 ||
		upstream.Options.TopK == nil || *upstream.Options.TopK != 40 ||
		upstream.Options.MinP == nil || *upstream.Options.MinP != 0.05 ||
		upstream.Options.Seed == nil || *upstream.Options.Seed != 42 {
		t.Fatalf("generation options were not forwarded: %+v", upstream.Options)
	}
	if len(response.Choices) != 1 || len(response.Choices[0].Message.ToolCalls) != 1 ||
		response.Choices[0].Message.ToolCalls[0].Type != "function" ||
		response.Choices[0].Message.ToolCalls[0].Function.Arguments != `{"city":"Moscow"}` {
		t.Fatalf("Ollama tool arguments were not normalized: %+v", response)
	}
}

func TestOllamaStreamsNativeToolCalls(t *testing.T) {
	var upstream ollamaChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte("{\"model\":\"llama3.2:latest\",\"message\":{\"role\":\"assistant\",\"tool_calls\":[{\"id\":\"call_1\",\"function\":{\"name\":\"weather.get\",\"arguments\":{\"city\":\"Moscow\"}}}]}}\n"))
		_, _ = w.Write([]byte("{\"model\":\"llama3.2:latest\",\"done\":true,\"done_reason\":\"stop\"}\n"))
	}))
	defer server.Close()

	var payloads []string
	topK := 20
	minP := 0.1
	response, err := NewOllama(server.URL, true).StreamChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model: "llama3.2:latest", Stream: true, Messages: []openai.Message{{Role: "user", Content: "weather"}},
		ChatGenerationOptions: openai.ChatGenerationOptions{TopK: &topK, MinP: &minP},
	}, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Choices[0].Message.ToolCalls) != 1 || response.Choices[0].Message.ToolCalls[0].Type != "function" {
		t.Fatalf("streamed tool call was not accumulated: %+v", response)
	}
	if upstream.Options.TopK == nil || *upstream.Options.TopK != 20 || upstream.Options.MinP == nil || *upstream.Options.MinP != 0.1 {
		t.Fatalf("streaming generation options were not forwarded: %+v", upstream.Options)
	}
	if len(payloads) != 2 || !strings.Contains(payloads[0], `"type":"function"`) ||
		!strings.Contains(payloads[0], `"arguments":"{\"city\":\"Moscow\"}"`) {
		t.Fatalf("unexpected streamed tool payloads: %v", payloads)
	}
}

func TestOllamaRejectsInvalidNativeSamplingOptions(t *testing.T) {
	invalidTopK := -1
	invalidMinP := 1.1
	for _, test := range []struct {
		name    string
		request openai.ChatCompletionRequest
		param   string
	}{
		{name: "top_k", request: openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{TopK: &invalidTopK}}, param: "top_k"},
		{name: "min_p", request: openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{MinP: &invalidMinP}}, param: "min_p"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var failure *Error
			err := NewOllama("http://unused.invalid", false).ValidateChatParameters(test.request)
			if !errors.As(err, &failure) || failure.UpstreamCode != "invalid_parameter" || failure.Param != test.param {
				t.Fatalf("invalid sampling option was not rejected: %v", err)
			}
		})
	}
}

func TestOllamaConvertsVisionContentToNativeImages(t *testing.T) {
	messages := ollamaMessages([]openai.Message{{Role: "user", Content: []any{
		map[string]any{"type": "text", "text": "describe"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo="}},
	}}})
	if len(messages) != 1 || messages[0].Content != "describe" || len(messages[0].Images) != 1 || messages[0].Images[0] != "iVBORw0KGgo=" {
		t.Fatalf("unexpected native Ollama vision message: %+v", messages)
	}
}

func TestOllamaEmbeddingsMapsNativeContract(t *testing.T) {
	promptTokens := 4
	var upstream ollamaEmbeddingRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(ollamaEmbeddingResponse{Model: "nomic-embed", Embeddings: [][]float64{{0.25, 0.75}, {0.5, 0.5}}, PromptEvalCount: &promptTokens})
	}))
	defer server.Close()
	dimensions := 2
	response, err := NewOllama(server.URL, false).Embeddings(context.Background(), openai.EmbeddingRequest{Model: "nomic-embed", Input: []any{"one", "two"}, Dimensions: &dimensions})
	if err != nil {
		t.Fatal(err)
	}
	if upstream.Dimensions == nil || *upstream.Dimensions != 2 {
		t.Fatalf("dimensions not forwarded: %+v", upstream)
	}
	if len(response.Data) != 2 || response.Data[1].Index != 1 || response.Usage.TotalTokens != 4 {
		t.Fatalf("unexpected mapped response: %+v", response)
	}
}

func TestOllamaStreamsChatCompletions(t *testing.T) {
	var upstreamRequest ollamaChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(ollamaChatResponse{
			Model:   "test-model",
			Message: openai.Message{Role: "assistant", Content: "hel"},
		})
		_ = json.NewEncoder(w).Encode(ollamaChatResponse{
			Model:   "test-model",
			Message: openai.Message{Role: "assistant", Content: "lo"},
		})
		_ = json.NewEncoder(w).Encode(ollamaChatResponse{
			Model:           "test-model",
			Done:            true,
			DoneReason:      "stop",
			PromptEvalCount: 4,
			EvalCount:       2,
		})
	}))
	defer server.Close()

	var payloads []string
	provider := NewOllama(server.URL, true)
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
	if len(payloads) != 3 {
		t.Fatalf("expected three streamed payloads, got %d: %v", len(payloads), payloads)
	}
	if !strings.Contains(payloads[0], `"content":"hel"`) || !strings.Contains(payloads[1], `"content":"lo"`) {
		t.Fatalf("unexpected streamed payloads: %v", payloads)
	}
	if !strings.Contains(payloads[2], `"finish_reason":"stop"`) {
		t.Fatalf("expected finish payload, got %s", payloads[2])
	}
	if openai.ContentText(response.Choices[0].Message.Content) != "hello" {
		t.Fatalf("unexpected content: %s", openai.ContentText(response.Choices[0].Message.Content))
	}
	if response.Usage.TotalTokens != 6 {
		t.Fatalf("unexpected total tokens: %d", response.Usage.TotalTokens)
	}
}

func TestOllamaResponses(t *testing.T) {
	maxOutputTokens := 11
	temperature := 0.2
	topP := 0.8
	parallel := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		var request openAICompatibleResponseRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "test-model" {
			t.Fatalf("unexpected model: %s", request.Model)
		}
		if request.Stream {
			t.Fatal("expected non-stream responses request")
		}
		if request.MaxOutputTokens == nil || *request.MaxOutputTokens != maxOutputTokens ||
			request.Temperature == nil || *request.Temperature != temperature ||
			request.TopP == nil || *request.TopP != topP || request.PreviousResponse != "resp-previous" ||
			request.ParallelToolCalls == nil || !*request.ParallelToolCalls || len(request.Tools) != 1 {
			t.Fatalf("Responses fields were not forwarded: %+v", request)
		}

		_ = json.NewEncoder(w).Encode(openai.ResponseResponse{
			ID:     "resp-test",
			Object: "response",
			Status: "completed",
			Model:  "test-model",
			Output: []openai.ResponseOutputItem{
				{
					Type:   "message",
					Status: "completed",
					Role:   "assistant",
					Content: []openai.ResponseOutputContent{
						{Type: "output_text", Text: "pong"},
					},
				},
			},
			Usage: openai.ResponseUsage{
				InputTokens:  3,
				OutputTokens: 1,
				TotalTokens:  4,
			},
		})
	}))
	defer server.Close()

	provider := NewOllama(server.URL, false)
	response, err := provider.Responses(context.Background(), openai.ResponseRequest{
		Model: "test-model", Input: "ping", Stream: true,
		Tools: []openai.ResponseTool{{Type: "function", Name: "weather.get"}}, ToolChoice: "required",
		ParallelToolCalls: &parallel, PreviousResponse: "resp-previous",
		MaxOutputTokens: &maxOutputTokens, Temperature: &temperature, TopP: &topP,
	})
	if err != nil {
		t.Fatal(err)
	}

	if response.OutputText != "pong" {
		t.Fatalf("unexpected output_text: %s", response.OutputText)
	}
	if response.Usage.TotalTokens != 4 {
		t.Fatalf("unexpected total tokens: %d", response.Usage.TotalTokens)
	}
}

func TestOllamaStreamsResponses(t *testing.T) {
	var upstreamRequest openAICompatibleResponseRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: response.output_text.delta` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","response_id":"resp-test","delta":"pon"}` + "\n\n"))
		_, _ = w.Write([]byte(`event: response.output_text.delta` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","response_id":"resp-test","delta":"g"}` + "\n\n"))
		_, _ = w.Write([]byte(`event: response.completed` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp-test","object":"response","status":"completed","model":"test-model","output":[{"type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"pong"}]}],"output_text":"pong"}}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	var events []string
	var payloads []string
	provider := NewOllama(server.URL, true)
	response, err := provider.StreamResponses(context.Background(), openai.ResponseRequest{
		Model:  "test-model",
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
	if len(payloads) != 3 {
		t.Fatalf("expected three streamed payloads, got %d: %v", len(payloads), payloads)
	}
	if events[0] != "response.output_text.delta" || events[2] != "response.completed" {
		t.Fatalf("unexpected events: %v", events)
	}
	if response.OutputText != "pong" {
		t.Fatalf("unexpected output_text: %s", response.OutputText)
	}
}
