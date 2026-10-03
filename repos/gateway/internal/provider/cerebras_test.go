package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestCerebrasChatMapsSupportedContractAndReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer cerebras-key" {
			t.Fatalf("path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "model" || body["max_completion_tokens"] != float64(32) || body["reasoning_effort"] != "high" || body["service_tier"] != "priority" || body["prompt_cache_key"] != "session-1" || body["prediction"] == nil || body["response_format"] == nil {
			t.Fatalf("request=%#v", body)
		}
		if body["logprobs"] != true || body["top_logprobs"] != float64(4) || body["parallel_tool_calls"] != true || len(body["tools"].([]any)) != 1 {
			t.Fatalf("request=%#v", body)
		}
		_, _ = fmt.Fprint(w, `{"id":"chat","object":"chat.completion","created":1,"model":"model","service_tier":"priority","choices":[{"index":0,"message":{"role":"assistant","reasoning":"plan","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`)
	}))
	defer server.Close()
	maxTokens, topLogprobs, logprobs, parallel := 32, 4, true, true
	client := NewCerebras(server.URL, "cerebras-key", true)
	response, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, MaxCompletionTokens: &maxTokens,
		ChatGenerationOptions: openai.ChatGenerationOptions{
			ReasoningEffort: "high", ServiceTier: "priority", PromptCacheKey: "session-1", Logprobs: &logprobs, TopLogprobs: &topLogprobs,
			Prediction: &openai.ChatPrediction{Type: "content", Content: "ok"},
		},
		ParallelToolCalls: &parallel,
		Tools:             []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "lookup", Parameters: map[string]any{"type": "object"}}}},
		ResponseFormat: func() *openai.ResponseFormat {
			strict := true
			return &openai.ResponseFormat{Type: "json_schema", JSONSchema: &openai.JSONSchemaFormat{Name: "answer", Schema: map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}}, "required": []string{"answer"}, "additionalProperties": false}, Strict: &strict}}
		}(),
	})
	if err != nil || response.Usage.TotalTokens != 5 || response.ServiceTier != "priority" || response.Choices[0].Message.ReasoningContent != "plan" || openai.ContentText(response.Choices[0].Message.Content) != "ok" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestCerebrasStreamsNormalizedReasoningAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["stream"] != true || body["stream_options"].(map[string]any)["include_usage"] != true {
			t.Fatalf("request=%#v", body)
		}
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat\",\"created\":1,\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning\":\"plan \",\"content\":\"ok\"}}]}\n\ndata: {\"id\":\"chat\",\"created\":1,\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\"done\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	client := NewCerebras(server.URL, "key", true)
	var payloads []string
	response, err := client.StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, Stream: true}, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil || response.Usage.TotalTokens != 5 || response.Choices[0].Message.ReasoningContent != "plan done" || len(payloads) != 2 {
		t.Fatalf("response=%+v payloads=%v err=%v", response, payloads, err)
	}
	for _, payload := range payloads {
		if strings.Contains(payload, `"reasoning":`) || !strings.Contains(payload, `"reasoning_content":`) {
			t.Fatalf("native reasoning was not normalized: %s", payload)
		}
	}
}

func TestCerebrasUsesEffectiveAutoServiceTier(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["service_tier"] != "auto" {
			t.Fatalf("request=%#v", body)
		}
		_, _ = fmt.Fprint(w, `{"id":"chat","object":"chat.completion","model":"model","service_tier":"auto","service_tier_used":"flex","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()
	client := NewCerebras(server.URL, "key", true)
	response, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}},
		ChatGenerationOptions: openai.ChatGenerationOptions{ServiceTier: "auto"},
	})
	if err != nil || response.ServiceTier != "flex" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestCerebrasStreamsEffectiveAutoServiceTier(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat\",\"model\":\"model\",\"service_tier\":\"auto\",\"service_tier_used\":\"priority\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"}}]}\n\ndata: {\"id\":\"chat\",\"model\":\"model\",\"service_tier\":\"auto\",\"service_tier_used\":\"priority\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	client := NewCerebras(server.URL, "key", true)
	var payloads []string
	response, err := client.StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, Stream: true,
		ChatGenerationOptions: openai.ChatGenerationOptions{ServiceTier: "auto"},
	}, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil || response.ServiceTier != "priority" || len(payloads) != 2 {
		t.Fatalf("response=%+v payloads=%v err=%v", response, payloads, err)
	}
	for _, payload := range payloads {
		if strings.Contains(payload, "service_tier_used") || !strings.Contains(payload, `"service_tier":"priority"`) {
			t.Fatalf("effective service tier was not normalized: %s", payload)
		}
	}
}

func TestCerebrasForwardsModelScopedClearThinking(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if value, found := body["clear_thinking"]; !found || value != false {
			t.Fatalf("request=%#v", body)
		}
		_, _ = fmt.Fprint(w, `{"id":"chat","object":"chat.completion","model":"zai-glm-4.7","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()
	clearThinking := false
	client := NewCerebras(server.URL, "key", true)
	_, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "zai-glm-4.7", Messages: []openai.Message{{Role: "user", Content: "hello"}},
		ChatGenerationOptions: openai.ChatGenerationOptions{ClearThinking: &clearThinking},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCerebrasRejectsClearThinkingForOtherModelsBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	clearThinking := true
	client := NewCerebras(server.URL, "key", true)
	_, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "gpt-oss-120b", Messages: []openai.Message{{Role: "user", Content: "hello"}},
		ChatGenerationOptions: openai.ChatGenerationOptions{ClearThinking: &clearThinking},
	})
	var failure *Error
	if !errors.As(err, &failure) || failure.Param != "clear_thinking" || failure.UpstreamCode != "invalid_request" || called {
		t.Fatalf("failure=%+v err=%v called=%v", failure, err, called)
	}
}

func TestCerebrasRejectsInvalidReasoningResponses(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
	}{
		{name: "wrong type", payload: `{"choices":[{"message":{"reasoning":{"text":"plan"}}}]}`},
		{name: "conflicting aliases", payload: `{"choices":[{"message":{"reasoning":"plan","reasoning_content":"other"}}]}`},
		{name: "oversized", payload: `{"choices":[{"message":{"reasoning":"` + strings.Repeat("x", openai.MaxChatReasoningContentBytes+1) + `"}}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := normalizeCerebrasChatPayload([]byte(test.payload), "message"); err == nil {
				t.Fatal("invalid provider reasoning was accepted")
			}
		})
	}
}

func TestCerebrasRejectsInvalidEffectiveServiceTier(t *testing.T) {
	for _, payload := range []string{
		`{"service_tier":"auto","service_tier_used":"unknown","choices":[]}`,
		`{"service_tier":"priority","service_tier_used":"flex","choices":[]}`,
		`{"service_tier":"auto","service_tier_used":7,"choices":[]}`,
	} {
		if _, err := normalizeCerebrasChatPayload([]byte(payload), "message"); err == nil {
			t.Fatalf("invalid provider service tier was accepted: %s", payload)
		}
	}
}

func TestCerebrasRejectsUnsupportedParametersBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewCerebras(server.URL, "key", true)
	store := true
	n := 2
	for _, test := range []struct {
		name, param string
		request     openai.ChatCompletionRequest
	}{
		{name: "store", param: "store", request: openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{Store: &store}}},
		{name: "multiple choices", param: "n", request: openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{N: &n}}},
		{name: "reasoning history", param: "messages.reasoning_content", request: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "assistant", Content: "answer", ReasoningContent: "plan"}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.request.Model = "model"
			if len(test.request.Messages) == 0 {
				test.request.Messages = []openai.Message{{Role: "user", Content: "hello"}}
			}
			_, err := client.ChatCompletions(t.Context(), test.request)
			var failure *Error
			if !errors.As(err, &failure) || failure.Provider != "cerebras" || failure.Param != test.param || called {
				t.Fatalf("failure=%+v err=%v called=%v", failure, err, called)
			}
		})
	}
}

func TestManagedCerebrasDiscoveryAndCapabilityProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer cerebras-key" {
			t.Fatalf("path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"model-b"},{"id":"model-a"}]}`)
	}))
	defer server.Close()
	router := New(Config{CredentialEncryptionKey: []byte("cerebras-test-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "cerebras", Type: "cerebras", BaseURL: server.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "key", ProviderID: "cerebras", Secret: "cerebras-key"}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(t.Context(), "cerebras", "key")
	if err != nil || len(models) != 2 || models[0].ID != "model-a" || models[1].ID != "model-b" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	for _, profile := range ManagedProviderCapabilityProfiles() {
		if profile.Type != "cerebras" {
			continue
		}
		if !slicesContain(profile.Operations, "chat") || !slicesContain(profile.Operations, "stream") || !slicesContain(profile.Capabilities, "tools") || !slicesContain(profile.Capabilities, "structured_output") || slicesContain(profile.Operations, "responses") {
			t.Fatalf("profile=%+v", profile)
		}
		if fmt.Sprint(profile.ChatParameters.ReasoningEffort) != "[none low medium high]" || fmt.Sprint(profile.ChatParameters.ServiceTier) != "[auto default flex priority]" {
			t.Fatalf("parameters=%+v", profile.ChatParameters)
		}
		if len(profile.ChatModelParameters) != 1 || profile.ChatModelParameters[0].Model != "zai-glm-4.7" || fmt.Sprint(profile.ChatModelParameters[0].SupportedOptions) != "[clear_thinking]" {
			t.Fatalf("model parameters=%+v", profile.ChatModelParameters)
		}
		return
	}
	t.Fatal("Cerebras capability profile is missing")
}
