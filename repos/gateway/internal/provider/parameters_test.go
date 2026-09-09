package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestNativeAdaptersRejectUnrepresentableChatParameters(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	seed := int64(0)
	parallel := false
	for _, tc := range []struct {
		adapter string
		field   string
		request openai.ChatCompletionRequest
	}{
		{"anthropic", "stop", openai.ChatCompletionRequest{Stop: 42}},
		{"anthropic", "seed", openai.ChatCompletionRequest{Seed: &seed}},
		{"ollama", "tool_choice", openai.ChatCompletionRequest{ToolChoice: "required"}},
		{"ollama", "parallel_tool_calls", openai.ChatCompletionRequest{ParallelToolCalls: &parallel}},
		{"anthropic", "service_tier", openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{ServiceTier: "priority"}}},
		{"ollama", "prompt_cache_key", openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{PromptCacheKey: "tenant-thread"}}},
		{"anthropic", "verbosity", openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{Verbosity: "low"}}},
	} {
		t.Run(tc.adapter+"/"+tc.field, func(t *testing.T) {
			var client interface {
				Client
				StreamingClient
			}
			if tc.adapter == "anthropic" {
				client = NewAnthropic(server.URL, "test", true)
			} else {
				client = NewOllama(server.URL, true)
			}
			_, err := client.ChatCompletions(context.Background(), tc.request)
			assertUnsupportedParameter(t, err, tc.field)
			_, err = client.StreamChatCompletions(context.Background(), tc.request, func(string) error { t.Error("unexpected stream output"); return nil })
			assertUnsupportedParameter(t, err, tc.field)
		})
	}
	if calls.Load() != 0 {
		t.Fatal("unsupported request reached upstream")
	}
}

func TestOtherAdaptersRejectMistralChatControlsBeforeUpstream(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	enabled := false
	for _, control := range []struct {
		name    string
		options openai.ChatGenerationOptions
	}{
		{name: "safe_prompt", options: openai.ChatGenerationOptions{SafePrompt: &enabled}},
		{name: "prompt_mode", options: openai.ChatGenerationOptions{PromptMode: "reasoning"}},
	} {
		for name, client := range map[string]Client{
			"openai-compatible": NewOpenAICompatible(server.URL, "key", false),
			"anthropic":         NewAnthropic(server.URL, "key", false),
			"ollama":            NewOllama(server.URL, false),
			"cohere":            NewCohere(server.URL, "key", false),
			"gemini":            NewGemini(server.URL, "key", false),
		} {
			t.Run(name+"/"+control.name, func(t *testing.T) {
				request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: control.options}
				_, err := client.ChatCompletions(t.Context(), request)
				assertUnsupportedParameter(t, err, control.name)
			})
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("unsupported Mistral Chat control reached upstream: %d", calls.Load())
	}
}

func TestNativeResponseAndEmbeddingParameterPolicy(t *testing.T) {
	for _, prompt := range []any{[]string{"one", "two"}, []int{1, 2}} {
		client := NewOllama("http://unused.invalid", true)
		request := openai.CompletionRequest{Model: "m", Prompt: prompt, Stream: true}
		_, err := client.Completions(context.Background(), request)
		assertUnsupportedParameter(t, err, "prompt")
		_, err = client.StreamCompletions(context.Background(), request, func(string) error { t.Error("unexpected stream output"); return nil })
		assertUnsupportedParameter(t, err, "prompt")
	}
	for _, tc := range []struct {
		adapter string
		field   string
		request openai.ResponseRequest
	}{
		{"anthropic", "previous_response_id", openai.ResponseRequest{PreviousResponse: "resp_other"}},
		{"anthropic", "safety_identifier", openai.ResponseRequest{SafetyIdentifier: "provider-user"}},
		{"ollama", "safety_identifier", openai.ResponseRequest{SafetyIdentifier: "provider-user"}},
		{"anthropic", "service_tier", openai.ResponseRequest{ServiceTier: "priority"}},
		{"ollama", "service_tier", openai.ResponseRequest{ServiceTier: "priority"}},
		{"anthropic", "prompt_cache_key", openai.ResponseRequest{PromptCacheKey: "tenant-thread"}},
		{"ollama", "prompt_cache_key", openai.ResponseRequest{PromptCacheKey: "tenant-thread"}},
		{"anthropic", "text.verbosity", openai.ResponseRequest{Text: map[string]any{"verbosity": "low"}}},
		{"ollama", "text.verbosity", openai.ResponseRequest{Text: map[string]any{"verbosity": "low"}}},
	} {
		var client interface {
			Client
			StreamingResponseClient
		}
		switch tc.adapter {
		case "anthropic":
			client = NewAnthropic("http://unused.invalid", "test", true)
		case "ollama":
			client = NewOllama("http://unused.invalid", true)
		}
		_, err := client.Responses(context.Background(), tc.request)
		assertUnsupportedParameter(t, err, tc.field)
		_, err = client.StreamResponses(context.Background(), tc.request, func(string, string) error { t.Error("unexpected event"); return nil })
		assertUnsupportedParameter(t, err, tc.field)
	}
	_, err := (Demo{}).Responses(context.Background(), openai.ResponseRequest{SafetyIdentifier: "provider-user"})
	assertUnsupportedParameter(t, err, "safety_identifier")
	_, err = (Demo{}).Responses(context.Background(), openai.ResponseRequest{ServiceTier: "priority"})
	assertUnsupportedParameter(t, err, "service_tier")
	_, err = (Demo{}).Responses(context.Background(), openai.ResponseRequest{PromptCacheKey: "tenant-thread"})
	assertUnsupportedParameter(t, err, "prompt_cache_key")
	_, err = (Demo{}).Responses(context.Background(), openai.ResponseRequest{Text: map[string]any{"verbosity": "low"}})
	assertUnsupportedParameter(t, err, "text.verbosity")
	_, err = NewOpenAICompatible("http://unused.invalid", "", true).ChatCompletions(context.Background(), openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{ServiceTier: "priority"}})
	assertUnsupportedParameter(t, err, "service_tier")
	_, err = NewOpenAICompatible("http://unused.invalid", "", true).StreamChatCompletions(context.Background(), openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{ServiceTier: "priority"}}, func(string) error { t.Error("unexpected event"); return nil })
	assertUnsupportedParameter(t, err, "service_tier")
	_, err = NewOpenAICompatible("http://unused.invalid", "", true).Responses(context.Background(), openai.ResponseRequest{ServiceTier: "priority"})
	assertUnsupportedParameter(t, err, "service_tier")
	_, err = NewOpenAICompatible("http://unused.invalid", "", true).StreamResponses(context.Background(), openai.ResponseRequest{ServiceTier: "priority"}, func(string, string) error { t.Error("unexpected event"); return nil })
	assertUnsupportedParameter(t, err, "service_tier")
	for _, tc := range []struct {
		field   string
		request openai.EmbeddingRequest
	}{
		{"user", openai.EmbeddingRequest{Model: "embed", Input: "text", User: "customer"}},
		{"encoding_format", openai.EmbeddingRequest{Model: "embed", Input: "text", EncodingFormat: "base64"}},
	} {
		_, err := NewOllama("http://unused.invalid", false).Embeddings(context.Background(), tc.request)
		assertUnsupportedParameter(t, err, tc.field)
	}
}

func assertUnsupportedParameter(t *testing.T, err error, param string) {
	t.Helper()
	var failure *Error
	if !errors.As(err, &failure) || failure.StatusCode != 400 || failure.Class != FailureClientRequest || failure.UpstreamCode != "unsupported_parameter" || failure.Param != param {
		t.Fatalf("expected terminal unsupported parameter %s, got %#v", param, err)
	}
}

func TestRouterValidatesParametersBeforeProviderModules(t *testing.T) {
	router := Router{endpoints: []Endpoint{{Name: "native", Type: "anthropic", Models: []string{"test"}, Provider: NewAnthropic("http://unused.invalid", "test", true), Capabilities: []string{"chat", "responses", "stream"}}}, modules: modules.NewPipeline([]modules.Module{rejectingModule{}}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}}
	seed := int64(1)
	req := modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "test", Seed: &seed}}
	_, err := router.ChatCompletions(context.Background(), req)
	assertUnsupportedParameter(t, err, "seed")
	_, _, err = router.StreamChatCompletions(context.Background(), req, func(string) error { return nil })
	assertUnsupportedParameter(t, err, "seed")
	req.ResponseRequest = &openai.ResponseRequest{Model: "test", PreviousResponse: "resp_old"}
	_, err = router.Responses(context.Background(), req)
	assertUnsupportedParameter(t, err, "previous_response_id")
	_, _, err = router.StreamResponses(context.Background(), req, func(string, string) error { return nil })
	assertUnsupportedParameter(t, err, "previous_response_id")

	router = Router{endpoints: []Endpoint{{Name: "compatible", Type: "openai-compatible", Models: []string{"test"}, Provider: NewOpenAICompatible("http://unused.invalid", "", true), Capabilities: []string{"chat", "responses", "stream"}}}, modules: modules.NewPipeline([]modules.Module{rejectingModule{}}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}}
	req = modules.RequestContext{Request: openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{ServiceTier: "priority"}, Model: "test"}}
	_, err = router.ChatCompletions(context.Background(), req)
	assertUnsupportedParameter(t, err, "service_tier")
	_, _, err = router.StreamChatCompletions(context.Background(), req, func(string) error { return nil })
	assertUnsupportedParameter(t, err, "service_tier")
	req.ResponseRequest = &openai.ResponseRequest{Model: "test", ServiceTier: "priority"}
	_, err = router.Responses(context.Background(), req)
	assertUnsupportedParameter(t, err, "service_tier")
	_, _, err = router.StreamResponses(context.Background(), req, func(string, string) error { return nil })
	assertUnsupportedParameter(t, err, "service_tier")
}

func TestNativeAdaptersRejectForeignToolSignatures(t *testing.T) {
	request := openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "assistant", ToolCalls: []openai.ToolCall{{ExtraContent: &openai.ToolCallExtraContent{Google: &openai.GoogleToolCallContent{ThoughtSignature: "opaque"}}}}}}}
	for _, client := range []Client{NewAnthropic("http://unused.invalid", "", true), NewOllama("http://unused.invalid", true), Demo{}} {
		assertUnsupportedParameter(t, validateChatAdapter(client, request), "messages.tool_calls.extra_content")
	}
}
