package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestRejectGenerationOptionsCoversEveryPublicField(t *testing.T) {
	optionsType := reflect.TypeOf(openai.ChatGenerationOptions{})
	for index := 0; index < optionsType.NumField(); index++ {
		field := optionsType.Field(index)
		parameter := strings.Split(field.Tag.Get("json"), ",")[0]
		if parameter == "" || parameter == "-" {
			t.Fatalf("ChatGenerationOptions.%s does not declare a public JSON parameter", field.Name)
		}
		t.Run(parameter, func(t *testing.T) {
			options := reflect.New(optionsType).Elem()
			value := options.FieldByIndex(field.Index)
			switch value.Kind() {
			case reflect.Map:
				value.Set(reflect.MakeMap(value.Type()))
			case reflect.Pointer:
				value.Set(reflect.New(value.Type().Elem()))
			case reflect.Slice:
				value.Set(reflect.MakeSlice(value.Type(), 0, 0))
			case reflect.String:
				value.SetString("supplied")
			default:
				t.Fatalf("ChatGenerationOptions.%s has unhandled kind %s", field.Name, value.Kind())
			}

			var failure *Error
			err := rejectGenerationOptions("contract-test", options.Interface().(openai.ChatGenerationOptions))
			if !errors.As(err, &failure) || failure.UpstreamCode != "unsupported_parameter" || failure.Param != parameter {
				t.Fatalf("public parameter %s is not covered by the adapter rejection policy: %v", parameter, err)
			}
		})
	}
}

func TestManagedOpenAIServiceTierIsValidatedAndForwarded(t *testing.T) {
	requests := 0
	expectedTiers := []string{"priority", "priority", "fast", "fast", "ultrafast", "ultrafast"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || requests >= len(expectedTiers) || body["service_tier"] != expectedTiers[requests] {
			t.Fatalf("service tier was not forwarded: %+v", body)
		}
		requests++
		switch r.URL.Path {
		case "/v1/chat/completions":
			_, _ = w.Write([]byte(`{"id":"chat","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		case "/v1/responses":
			_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","model":"m","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := providerFor(config.ProviderEndpointConfig{Type: "openai", BaseURL: server.URL})
	for _, tier := range []string{"priority", "fast", "ultrafast"} {
		request := openai.ChatCompletionRequest{Model: "m", Messages: []openai.Message{{Role: "user", Content: "test"}}, ChatGenerationOptions: openai.ChatGenerationOptions{ServiceTier: tier}}
		if _, err := client.ChatCompletions(t.Context(), request); err != nil {
			t.Fatalf("chat service_tier=%s: %v", tier, err)
		}
		if _, err := client.Responses(t.Context(), openai.ResponseRequest{Model: "m", Input: "test", ServiceTier: tier}); err != nil {
			t.Fatalf("responses service_tier=%s: %v", tier, err)
		}
	}
	request := openai.ChatCompletionRequest{Model: "m", Messages: []openai.Message{{Role: "user", Content: "test"}}, ChatGenerationOptions: openai.ChatGenerationOptions{ServiceTier: "scale"}}
	if err := validateChatAdapter(client, request); err == nil || requests != 6 {
		t.Fatalf("unsupported tier reached provider: err=%v requests=%d", err, requests)
	}
}

func TestManagedOpenAIDefaultReasoningEffortIsForwarded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["reasoning_effort"] != "default" {
			t.Fatalf("reasoning effort was not forwarded: %+v", body)
		}
		_, _ = w.Write([]byte(`{"id":"chat","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()
	client := providerFor(config.ProviderEndpointConfig{Type: "openai", BaseURL: server.URL})
	request := openai.ChatCompletionRequest{Model: "m", Messages: []openai.Message{{Role: "user", Content: "test"}}, ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: "default"}}
	if _, err := client.ChatCompletions(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}

func TestChatReasoningContentSupportIsExplicit(t *testing.T) {
	request := openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "assistant", ReasoningContent: "plan"}}}
	unsupported := []struct {
		name     string
		validate func(openai.ChatCompletionRequest) error
	}{
		{name: "anthropic", validate: (Anthropic{}).ValidateChatParameters},
		{name: "bedrock", validate: (Bedrock{}).ValidateChatParameters},
		{name: "cohere", validate: (Cohere{}).ValidateChatParameters},
		{name: "demo", validate: (Demo{}).ValidateChatParameters},
		{name: "gemini", validate: (Gemini{}).ValidateChatParameters},
	}
	for _, adapter := range unsupported {
		t.Run(adapter.name, func(t *testing.T) {
			var failure *Error
			if err := adapter.validate(request); !errors.As(err, &failure) || failure.Param != "messages.reasoning_content" || failure.UpstreamCode != "unsupported_parameter" {
				t.Fatalf("reasoning_content was not rejected explicitly: %v", err)
			}
		})
	}

	for _, adapter := range []struct {
		name     string
		validate func(openai.ChatCompletionRequest) error
	}{
		{name: "compatible", validate: NewOpenAICompatible("http://unused.invalid", "", false).ValidateChatParameters},
		{name: "deepseek", validate: NewDeepSeek("http://unused.invalid", "", false).ValidateChatParameters},
		{name: "groq", validate: NewGroq("http://unused.invalid", "", false).ValidateChatParameters},
		{name: "mistral", validate: NewMistral("http://unused.invalid", "", false).ValidateChatParameters},
		{name: "ollama", validate: NewOllama("http://unused.invalid", false).ValidateChatParameters},
	} {
		t.Run(adapter.name, func(t *testing.T) {
			if err := adapter.validate(request); err != nil {
				t.Fatalf("reasoning_content rejected: %v", err)
			}
		})
	}
}

func TestDemoRejectsIgnoredResponseParameters(t *testing.T) {
	limit, sample, parallel := 16, 0.5, true
	for _, test := range []struct {
		name    string
		request openai.ResponseRequest
	}{
		{name: "instructions", request: openai.ResponseRequest{Instructions: "be concise"}},
		{name: "tools", request: openai.ResponseRequest{Tools: []openai.ResponseTool{{Type: "function", Name: "lookup"}}}},
		{name: "tool_choice", request: openai.ResponseRequest{ToolChoice: "none"}},
		{name: "parallel_tool_calls", request: openai.ResponseRequest{ParallelToolCalls: &parallel}},
		{name: "text", request: openai.ResponseRequest{Text: map[string]any{"format": map[string]any{"type": "json_object"}}}},
		{name: "previous_response_id", request: openai.ResponseRequest{PreviousResponse: "resp_previous"}},
		{name: "max_output_tokens", request: openai.ResponseRequest{MaxOutputTokens: &limit}},
		{name: "max_tokens", request: openai.ResponseRequest{MaxTokens: &limit}},
		{name: "temperature", request: openai.ResponseRequest{Temperature: &sample}},
		{name: "top_p", request: openai.ResponseRequest{TopP: &sample}},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.request.Model, test.request.Input = "demo", "hello"
			var failure *Error
			if err := (Demo{}).ValidateResponseParameters(test.request); !errors.As(err, &failure) || failure.Param != test.name || failure.UpstreamCode != "unsupported_parameter" {
				t.Fatalf("ignored parameter was not rejected: %v", err)
			}
		})
	}
}

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

func TestAdaptersWithoutAssistantPrefillRejectMessagePrefixBeforeUpstream(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	prefix := false
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "assistant", Content: "history", Prefix: &prefix}}}
	for name, client := range map[string]Client{
		"openai-compatible": NewOpenAICompatible(server.URL, "key", false),
		"ollama":            NewOllama(server.URL, false),
		"cohere":            NewCohere(server.URL, "key", false),
		"gemini":            NewGemini(server.URL, "key", false),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := client.ChatCompletions(t.Context(), request)
			assertUnsupportedParameter(t, err, "messages.prefix")
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("unsupported message prefix reached upstream: %d", calls.Load())
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
		{"anthropic", "user", openai.ResponseRequest{User: "provider-user"}},
		{"ollama", "user", openai.ResponseRequest{User: "provider-user"}},
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
	_, err = (Demo{}).Responses(context.Background(), openai.ResponseRequest{User: "provider-user"})
	assertUnsupportedParameter(t, err, "user")
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
	_, err = (Demo{}).Embeddings(context.Background(), openai.EmbeddingRequest{Model: "embed", Input: "text", User: "customer"})
	assertUnsupportedParameter(t, err, "user")
}

func TestOllamaRejectsUnsupportedResponsesControlsBeforeUpstream(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	trueValue := true
	falseValue := false
	truncation := "auto"
	topLogprobs := 1
	context := "auto"
	mode := "standard"
	summary := "auto"
	generateSummary := "auto"
	for _, test := range []struct {
		name    string
		request openai.ResponseRequest
	}{
		{name: "previous_response_id", request: openai.ResponseRequest{PreviousResponse: "resp_prior"}},
		{name: "conversation", request: openai.ResponseRequest{Conversation: &openai.ResponseConversation{ID: "conv_prior"}}},
		{name: "truncation", request: openai.ResponseRequest{Truncation: &truncation}},
		{name: "store", request: openai.ResponseRequest{Store: &trueValue}},
		{name: "include", request: openai.ResponseRequest{Include: []string{"reasoning.encrypted_content"}}},
		{name: "metadata", request: openai.ResponseRequest{Metadata: map[string]string{"trace": "one"}}},
		{name: "top_logprobs", request: openai.ResponseRequest{TopLogprobs: &topLogprobs}},
		{name: "tool_choice", request: openai.ResponseRequest{ToolChoice: "required"}},
		{name: "parallel_tool_calls", request: openai.ResponseRequest{ParallelToolCalls: &falseValue}},
		{name: "reasoning.context", request: openai.ResponseRequest{Reasoning: &openai.ResponseReasoning{Context: &context}}},
		{name: "reasoning.mode", request: openai.ResponseRequest{Reasoning: &openai.ResponseReasoning{Mode: &mode}}},
		{name: "reasoning.summary", request: openai.ResponseRequest{Reasoning: &openai.ResponseReasoning{Summary: &summary}}},
		{name: "reasoning.generate_summary", request: openai.ResponseRequest{Reasoning: &openai.ResponseReasoning{GenerateSummary: &generateSummary}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := test.request
			request.Model = "model"
			request.Input = "hello"
			client := NewOllama(server.URL, true)
			_, err := client.Responses(t.Context(), request)
			assertUnsupportedParameter(t, err, test.name)
			_, err = client.StreamResponses(t.Context(), request, func(string, string) error { t.Error("unexpected stream output"); return nil })
			assertUnsupportedParameter(t, err, test.name)
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("unsupported Ollama requests reached upstream: %d", calls.Load())
	}
}

func TestNativeAdaptersRejectResponseContextManagement(t *testing.T) {
	threshold := 1_000
	request := openai.ResponseRequest{
		Model: "model",
		Input: "hello",
		ContextManagement: []openai.ResponseContextEntry{{
			Type:             "compaction",
			CompactThreshold: &threshold,
		}},
	}
	for name, validate := range map[string]func(openai.ResponseRequest) error{
		"anthropic": (Anthropic{}).ValidateResponseParameters,
		"deepseek":  (DeepSeek{}).ValidateResponseParameters,
		"demo":      (Demo{}).ValidateResponseParameters,
		"groq":      (Groq{}).ValidateResponseParameters,
		"ollama":    (Ollama{}).ValidateResponseParameters,
		"xai":       (XAI{}).ValidateResponseParameters,
	} {
		t.Run(name, func(t *testing.T) {
			assertUnsupportedParameter(t, validate(request), "context_management")
		})
	}
}

func TestNativeAdaptersRejectResponsesProviderModeration(t *testing.T) {
	request := openai.ResponseRequest{Model: "model", Input: "hello", Moderation: &openai.ProviderModeration{Model: "moderation"}}
	for name, validate := range map[string]func(openai.ResponseRequest) error{
		"anthropic": (Anthropic{}).ValidateResponseParameters,
		"deepseek":  (DeepSeek{}).ValidateResponseParameters,
		"demo":      (Demo{}).ValidateResponseParameters,
		"groq":      (Groq{}).ValidateResponseParameters,
		"ollama":    (Ollama{}).ValidateResponseParameters,
		"xai":       (XAI{}).ValidateResponseParameters,
	} {
		t.Run(name, func(t *testing.T) {
			assertUnsupportedParameter(t, validate(request), "moderation")
		})
	}
}

func TestNativeAdaptersRejectChatProviderModeration(t *testing.T) {
	request := openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{Moderation: &openai.ProviderModeration{Model: "moderation"}}, Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}
	for name, validate := range map[string]func(openai.ChatCompletionRequest) error{
		"anthropic":  (Anthropic{}).ValidateChatParameters,
		"bedrock":    (Bedrock{}).ValidateChatParameters,
		"cerebras":   (Cerebras{}).ValidateChatParameters,
		"cohere":     (Cohere{}).ValidateChatParameters,
		"deepseek":   (DeepSeek{}).ValidateChatParameters,
		"demo":       (Demo{}).ValidateChatParameters,
		"gemini":     (Gemini{}).ValidateChatParameters,
		"groq":       (Groq{}).ValidateChatParameters,
		"mistral":    (Mistral{}).ValidateChatParameters,
		"nvidia-nim": (NVIDIANIM{}).ValidateChatParameters,
		"ollama":     (Ollama{}).ValidateChatParameters,
		"openrouter": (OpenRouter{}).ValidateChatParameters,
		"together":   (Together{}).ValidateChatParameters,
		"vertex":     (VertexGemini{}).ValidateChatParameters,
		"xai":        (XAI{}).ValidateChatParameters,
	} {
		t.Run(name, func(t *testing.T) {
			assertUnsupportedParameter(t, validate(request), "moderation")
		})
	}
}

func TestOtherEmbeddingAdaptersRejectMistralMetadata(t *testing.T) {
	request := openai.EmbeddingRequest{Model: "embed", Input: "text", Metadata: map[string]string{"trace": "one"}}
	for name, call := range map[string]func() error{
		"openai-compatible": func() error {
			_, err := NewOpenAICompatible("http://unused.invalid", "", false).Embeddings(t.Context(), request)
			return err
		},
		"ollama": func() error {
			_, err := NewOllama("http://unused.invalid", false).Embeddings(t.Context(), request)
			return err
		},
		"demo": func() error {
			_, err := (Demo{}).Embeddings(t.Context(), request)
			return err
		},
		"gemini": func() error {
			_, err := NewGemini("http://unused.invalid", "", false).Embeddings(t.Context(), request)
			return err
		},
		"cohere": func() error {
			cohereRequest := request
			cohereRequest.InputType = "search_query"
			_, err := NewCohere("http://unused.invalid", "").Embeddings(t.Context(), cohereRequest)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			assertUnsupportedParameter(t, call(), "metadata")
		})
	}
}

func TestOtherEmbeddingAdaptersRejectMistralOutputDType(t *testing.T) {
	request := openai.EmbeddingRequest{Model: "embed", Input: "text", OutputDType: "int8"}
	for name, call := range map[string]func() error{
		"openai-compatible": func() error {
			_, err := NewOpenAICompatible("http://unused.invalid", "", false).Embeddings(t.Context(), request)
			return err
		},
		"ollama": func() error {
			_, err := NewOllama("http://unused.invalid", false).Embeddings(t.Context(), request)
			return err
		},
		"demo": func() error {
			_, err := (Demo{}).Embeddings(t.Context(), request)
			return err
		},
		"gemini": func() error {
			_, err := NewGemini("http://unused.invalid", "", false).Embeddings(t.Context(), request)
			return err
		},
		"cohere": func() error {
			cohereRequest := request
			cohereRequest.InputType = "search_query"
			_, err := NewCohere("http://unused.invalid", "").Embeddings(t.Context(), cohereRequest)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			assertUnsupportedParameter(t, call(), "output_dtype")
		})
	}
}

func TestOtherCompletionAdaptersRejectMistralFIMControlsBeforeUpstream(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	minimum := 1
	for _, control := range []struct {
		name    string
		request openai.CompletionRequest
	}{
		{name: "metadata", request: openai.CompletionRequest{Model: "model", Prompt: "x", Metadata: map[string]string{"ticket": "42"}}},
		{name: "min_tokens", request: openai.CompletionRequest{Model: "model", Prompt: "x", MinTokens: &minimum}},
		{name: "prompt_cache_key", request: openai.CompletionRequest{Model: "model", Prompt: "x", PromptCacheKey: "prefix"}},
	} {
		for name, client := range map[string]CompletionClient{
			"openai-compatible": NewOpenAICompatible(server.URL, "key", true),
			"ollama":            NewOllama(server.URL, true),
		} {
			t.Run(name+"/"+control.name, func(t *testing.T) {
				_, err := client.Completions(t.Context(), control.request)
				assertUnsupportedParameter(t, err, control.name)
			})
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("unsupported FIM controls reached upstream: %d", calls.Load())
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
