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

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestMistralFIMThroughRouter(t *testing.T) {
	if NewMistral("", "", false).SupportsResponses() {
		t.Fatal("unverified Responses transport advertised")
	}
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/proxy/v1/fim/completions" || r.Header.Get("Authorization") != "Bearer provider-key" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("unexpected request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["model"] != "codestral-upstream" || request["prompt"] != "masked" || request["suffix"] != "return sum" || request["max_tokens"] != float64(40) || request["random_seed"] != float64(7) || request["stream"] != false {
			t.Fatalf("FIM request fields changed: %+v", request)
		}
		if _, found := request["seed"]; found {
			t.Fatal("compatible seed field leaked into native FIM request")
		}
		_, _ = fmt.Fprint(w, `{"id":"fim-1","object":"chat.completion","created":10,"model":"codestral-upstream","choices":[{"index":0,"message":{"role":"assistant","content":"(a, b) { return sum }"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":6,"total_tokens":14}}`)
	}))
	defer server.Close()

	maxTokens := 40
	seed := int64(7)
	request := openai.CompletionRequest{Model: "codestral-public", Prompt: "func add", Suffix: "return sum", MaxTokens: &maxTokens, Seed: &seed}
	lifecycle := &completionLifecycleModule{}
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "mistral-native", Type: "mistral", BaseURL: server.URL + "/proxy", APIKey: "provider-key",
		Models: []string{"codestral-public"}, ModelAliases: map[string]string{"codestral-public": "codestral-upstream"}, Capabilities: []string{"chat"},
	}}, Modules: modules.NewPipeline([]modules.Module{lifecycle})}).(*Router)
	response, err := router.Completions(context.Background(), modules.RequestContext{
		CompletionRequest: &request, Request: openai.ChatCompletionRequest{Model: request.Model, Messages: []openai.Message{{Role: "user", Content: request.Prompt}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || lifecycle.pre != 1 || lifecycle.post != 1 || lifecycle.response == nil || lifecycle.response.Usage.TotalTokens != 14 || response.Object != "text_completion" || response.Model != "codestral-upstream" || len(response.Choices) != 1 || response.Choices[0].Text != "(a, b) { return sum }" || response.Usage.TotalTokens != 14 {
		t.Fatalf("response=%+v calls=%d", response, calls.Load())
	}
}

func TestMistralFIMStreamingThroughRouter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/fim/completions" || r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("unexpected stream request: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"fim-stream\",\"object\":\"chat.completion.chunk\",\"created\":20,\"model\":\"codestral\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"first\"},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"fim-stream\",\"object\":\"chat.completion.chunk\",\"created\":20,\"model\":\"codestral\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" second\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	request := openai.CompletionRequest{Model: "codestral", Prompt: "start", Stream: true}
	lifecycle := &completionLifecycleModule{}
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "mistral", Type: "mistral", BaseURL: server.URL, Stream: true, Models: []string{"codestral"}, Capabilities: []string{"chat", "stream"}}}, Modules: modules.NewPipeline([]modules.Module{lifecycle})}).(*Router)
	payloads := []string{}
	response, streamed, err := router.StreamCompletions(context.Background(), modules.RequestContext{CompletionRequest: &request, Request: openai.ChatCompletionRequest{Model: request.Model, Stream: true, Messages: []openai.Message{{Role: "user", Content: request.Prompt}}}}, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil || !streamed {
		t.Fatalf("streamed=%v err=%v", streamed, err)
	}
	if lifecycle.pre != 1 || lifecycle.post != 1 || lifecycle.response == nil || lifecycle.response.Usage.TotalTokens != 5 || len(payloads) != 2 || !strings.Contains(payloads[0], `"object":"text_completion"`) || !strings.Contains(payloads[1], `"finish_reason":"stop"`) || response.Choices[0].Text != "first second" || response.Usage.TotalTokens != 5 {
		t.Fatalf("payloads=%v response=%+v", payloads, response)
	}
}

func TestMistralFIMRejectsUnsupportedParametersBeforeUpstream(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	one, enabled := 1, true
	for _, test := range []struct {
		name, param string
		request     openai.CompletionRequest
	}{
		{name: "prompt list", param: "prompt", request: openai.CompletionRequest{Prompt: []string{"one"}}},
		{name: "best of", param: "best_of", request: openai.CompletionRequest{Prompt: "one", BestOf: &one}},
		{name: "echo", param: "echo", request: openai.CompletionRequest{Prompt: "one", Echo: &enabled}},
		{name: "n", param: "n", request: openai.CompletionRequest{Prompt: "one", N: &one}},
		{name: "logprobs", param: "logprobs", request: openai.CompletionRequest{Prompt: "one", Logprobs: &one}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewMistral(server.URL, "key", false).Completions(context.Background(), test.request)
			var failure *Error
			if !errors.As(err, &failure) || failure.Param != test.param || failure.UpstreamCode != "unsupported_parameter" {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("unsupported request reached Mistral")
	}
}

func TestMistralFIMRejectsTruncatedStream(t *testing.T) {
	stream := `data: {"id":"fim","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"stop"}]}` + "\n\n"
	_, err := streamMistralFIM(strings.NewReader(stream), openai.CompletionRequest{Prompt: "p"}, nil)
	if err == nil || !strings.Contains(err.Error(), "before [DONE]") {
		t.Fatalf("truncated stream accepted: %v", err)
	}
}

func TestMistralFIMRejectsNonStringContentAndChangedStreamIdentity(t *testing.T) {
	invalidResponse := `{"id":"fim","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":{"text":"hidden"}},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	if _, err := decodeMistralFIM(strings.NewReader(invalidResponse), openai.CompletionRequest{Prompt: "p"}); err == nil {
		t.Fatal("non-string FIM content accepted")
	}
	changedIdentity := strings.Join([]string{
		`data: {"id":"first","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null}]}`,
		`data: {"id":"second","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"
	if _, err := streamMistralFIM(strings.NewReader(changedIdentity), openai.CompletionRequest{Prompt: "p"}, nil); err == nil || !strings.Contains(err.Error(), "changed stream ID") {
		t.Fatalf("changed stream identity accepted: %v", err)
	}
}

func TestMistralChatUsesNativeRandomSeedInJSONAndStreaming(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer provider-key" {
			t.Errorf("unexpected request: %s headers=%v", r.URL.Path, r.Header)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["random_seed"] != float64(17) {
			t.Fatalf("random_seed=%#v", request["random_seed"])
		}
		expectedSafePrompt := request["stream"] != true
		if request["safe_prompt"] != expectedSafePrompt {
			t.Fatalf("safe_prompt=%#v want %v", request["safe_prompt"], expectedSafePrompt)
		}
		if request["prompt_mode"] != "reasoning" {
			t.Fatalf("prompt_mode=%#v", request["prompt_mode"])
		}
		if _, found := request["seed"]; found {
			t.Fatalf("generic seed leaked into Mistral request: %#v", request)
		}
		if request["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-stream\",\"object\":\"chat.completion.chunk\",\"created\":20,\"model\":\"mistral-small\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"streamed\"},\"finish_reason\":null}]}\n\n")
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-stream\",\"object\":\"chat.completion.chunk\",\"created\":20,\"model\":\"mistral-small\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		_, _ = fmt.Fprint(w, `{"id":"chat-json","object":"chat.completion","created":10,"model":"mistral-small","choices":[{"index":0,"message":{"role":"assistant","content":"json"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer server.Close()

	seed := int64(17)
	safePrompt := true
	client := NewMistral(server.URL, "provider-key", true)
	request := openai.ChatCompletionRequest{Model: "mistral-small", Messages: []openai.Message{{Role: "user", Content: "hello"}}, Seed: &seed, ChatGenerationOptions: openai.ChatGenerationOptions{SafePrompt: &safePrompt, PromptMode: "reasoning"}}
	response, err := client.ChatCompletions(t.Context(), request)
	if err != nil || openai.ContentText(response.Choices[0].Message.Content) != "json" || response.Usage.TotalTokens != 3 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	safePrompt = false
	payloads := []string{}
	streamed, err := client.StreamChatCompletions(t.Context(), request, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil || streamed.Usage.TotalTokens != 4 || openai.ContentText(streamed.Choices[0].Message.Content) != "streamed" || len(payloads) != 2 || calls.Load() != 2 {
		t.Fatalf("streamed=%+v payloads=%v calls=%d err=%v", streamed, payloads, calls.Load(), err)
	}
}

func TestMistralEmbeddingsAndErrorsUseNativeProviderIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/embeddings":
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request["output_dimension"] != float64(2) || request["encoding_format"] != "float" {
				t.Fatalf("native embedding request=%#v", request)
			}
			if _, found := request["dimensions"]; found {
				t.Fatalf("generic dimensions leaked into Mistral request: %#v", request)
			}
			_, _ = fmt.Fprint(w, `{"object":"list","model":"mistral-embed","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`)
		case "/v1/chat/completions":
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(w, `{"error":{"type":"rate_limit_error"}}`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewMistral(server.URL, "provider-key", false)
	dimensions := 2
	embedding, err := client.Embeddings(t.Context(), openai.EmbeddingRequest{Model: "mistral-embed", Input: "hello", EncodingFormat: "float", Dimensions: &dimensions})
	if err != nil || !embedding.UsageReported || embedding.Usage.TotalTokens != 2 || len(embedding.Data) != 1 || len(embedding.Data[0].Embedding) != 2 {
		t.Fatalf("embedding=%+v err=%v", embedding, err)
	}
	_, err = client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "mistral-small", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
	var failure *Error
	if !errors.As(err, &failure) || failure.Provider != "mistral" || failure.Class != FailureRateLimit || failure.UpstreamCode != "rate_limit_error" || failure.RetryAfter <= 0 {
		t.Fatalf("error=%v failure=%+v", err, failure)
	}
	inputType := openai.EmbeddingRequest{Model: "mistral-embed", Input: "hello", InputType: "search_query"}
	if err := client.ValidateEmbeddingParameters(inputType); !errors.As(err, &failure) || failure.Provider != "mistral" || failure.Param != "input_type" {
		t.Fatalf("validation error=%v", err)
	}
}

func TestMistralEmbeddingsRejectUnsupportedInputBeforeUpstream(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	client := NewMistral(server.URL, "provider-key", false)
	for _, test := range []struct {
		name, param string
		request     openai.EmbeddingRequest
	}{
		{name: "token IDs", param: "input", request: openai.EmbeddingRequest{Model: "mistral-embed", Input: []any{1.0, 2.0}}},
		{name: "task type", param: "input_type", request: openai.EmbeddingRequest{Model: "mistral-embed", Input: "text", InputType: "search_query"}},
		{name: "user", param: "user", request: openai.EmbeddingRequest{Model: "mistral-embed", Input: "text", User: "provider-hint"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.Embeddings(t.Context(), test.request)
			var failure *Error
			if !errors.As(err, &failure) || failure.Provider != "mistral" || failure.Param != test.param || failure.UpstreamCode != "unsupported_parameter" {
				t.Fatalf("error=%v failure=%+v", err, failure)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("unsupported embedding requests reached Mistral: %d", calls.Load())
	}
}

func TestMistralModerationsNormalizeTextContractAndRejectStructuredInput(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/moderations" || r.Header.Get("Authorization") != "Bearer provider-key" {
			t.Fatalf("unexpected moderation request: %s %s", r.Method, r.URL.Path)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["model"] != "mistral-moderation" || request["input"] != "inspect me" {
			t.Fatalf("request=%#v err=%v", request, err)
		}
		_, _ = fmt.Fprint(w, `{"id":"mod-1","model":"mistral-moderation","results":[{"flagged":false,"categories":{"violence":false,"pii":false},"category_scores":{"violence":0.1,"pii":0.2}}]}`)
	}))
	defer server.Close()

	client := NewMistral(server.URL, "provider-key", false)
	response, err := client.Moderations(t.Context(), openai.ModerationRequest{Model: "mistral-moderation", Input: "inspect me"})
	if err != nil || response.ID != "mod-1" || len(response.Results) != 1 || len(response.Results[0].CategoryAppliedInputTypes) != 2 || response.Results[0].CategoryAppliedInputTypes["violence"][0] != "text" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	structured := []any{map[string]any{"type": "text", "text": "inspect me"}}
	_, err = client.Moderations(t.Context(), openai.ModerationRequest{Model: "mistral-moderation", Input: structured})
	var failure *Error
	if !errors.As(err, &failure) || failure.Provider != "mistral" || failure.Param != "input" || failure.UpstreamCode != "unsupported_parameter" || calls.Load() != 1 {
		t.Fatalf("error=%v failure=%+v calls=%d", err, failure, calls.Load())
	}
}

func TestMistralDoesNotAdvertiseInheritedRerankTransport(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "mistral", Type: "mistral", BaseURL: server.URL, Models: []string{"mistral-rerank"}, Capabilities: []string{"rerank"},
	}}}).(*Router)
	request := openai.RerankRequest{Model: "mistral-rerank", Query: "query", Documents: []any{"document"}}
	_, err := router.Rerank(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: request.Model}, RerankRequest: &request})
	if err == nil || !strings.Contains(err.Error(), "no rerank endpoint") || calls.Load() != 0 {
		t.Fatalf("error=%v upstream calls=%d", err, calls.Load())
	}
}

func TestMistralRejectsChatWebSearchBeforeExecution(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	client := NewMistral(server.URL, "provider-key", true)
	request := openai.ChatCompletionRequest{
		Model: "mistral-small", Messages: []openai.Message{{Role: "user", Content: "latest news"}},
		ChatGenerationOptions: openai.ChatGenerationOptions{WebSearchOptions: &openai.ChatWebSearchOptions{}},
	}
	for name, call := range map[string]func() error{
		"adapter validation": func() error { return validateChatAdapter(client, request) },
		"json":               func() error { _, err := client.ChatCompletions(t.Context(), request); return err },
		"stream":             func() error { _, err := client.StreamChatCompletions(t.Context(), request, nil); return err },
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			var failure *Error
			if !errors.As(err, &failure) || failure.Provider != "mistral" || failure.Param != "web_search_options" || failure.UpstreamCode != "unsupported_parameter" {
				t.Fatalf("error=%v failure=%+v", err, failure)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("unsupported search reached Mistral: %d", calls.Load())
	}
}
