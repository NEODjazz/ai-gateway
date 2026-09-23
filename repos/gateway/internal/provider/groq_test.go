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

func TestGroqChatMapsSupportedContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer groq-key" {
			t.Fatalf("path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "qwen/qwen3.8-27b" || body["max_completion_tokens"] != float64(32) || body["service_tier"] != "performance" || body["user"] != "tenant-user" || body["reasoning_format"] != "parsed" || len(body["tools"].([]any)) != 1 || body["response_format"] == nil {
			t.Fatalf("request=%#v", body)
		}
		_, _ = fmt.Fprint(w, `{"id":"chat","object":"chat.completion","model":"model","service_tier":"performance","choices":[{"index":0,"message":{"role":"assistant","content":"ok","reasoning":"private plan"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer server.Close()
	maxTokens := 32
	client := NewGroq(server.URL+"/openai/v1", "groq-key", true)
	response, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "qwen/qwen3.8-27b", Messages: []openai.Message{{Role: "user", Content: "hello"}}, MaxCompletionTokens: &maxTokens, ChatGenerationOptions: openai.ChatGenerationOptions{ServiceTier: "performance", User: "tenant-user", ReasoningFormat: "parsed"},
		Tools:          []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "lookup", Parameters: map[string]any{"type": "object"}}}},
		ResponseFormat: &openai.ResponseFormat{Type: "json_object"},
	})
	if err != nil || response.Usage.TotalTokens != 3 || response.ServiceTier != "performance" || openai.ContentText(response.Choices[0].Message.Content) != "ok" || response.Choices[0].Message.ReasoningContent != "private plan" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestGroqAcceptsDocumentedServiceTiers(t *testing.T) {
	client := NewGroq("http://unused.invalid", "", false)
	for _, tier := range []string{"auto", "on_demand", "flex", "performance"} {
		t.Run(tier, func(t *testing.T) {
			request := openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{ServiceTier: tier}}
			if err := client.ValidateChatParameters(request); err != nil {
				t.Fatalf("documented service tier rejected: %v", err)
			}
		})
	}
}

func TestGroqRejectsResponseOnlyServiceTierBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewGroq(server.URL, "key", true)
	_, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}},
		ChatGenerationOptions: openai.ChatGenerationOptions{ServiceTier: "default"},
	})
	var providerErr *Error
	if !errors.As(err, &providerErr) || providerErr.Param != "service_tier" || providerErr.UpstreamCode != "unsupported_parameter" || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestGroqRejectsUnsupportedParametersBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewGroq(server.URL, "key", true)
	logprobs := true
	_, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{Logprobs: &logprobs}})
	if err == nil || !strings.Contains(err.Error(), "logprobs") || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestGroqStreamsWithUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["include_reasoning"] != true {
			t.Fatalf("request=%#v", body)
		}
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat\",\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning\":\"private \"}}]}\n\ndata: {\"id\":\"chat\",\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\"plan\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	client := NewGroq(server.URL, "key", true)
	var payloads []string
	includeReasoning := true
	response, err := client.StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "openai/gpt-oss-20b", Messages: []openai.Message{{Role: "user", Content: "hello"}}, Stream: true, ChatGenerationOptions: openai.ChatGenerationOptions{IncludeReasoning: &includeReasoning}}, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil || response.Usage.TotalTokens != 3 || response.Choices[0].Message.ReasoningContent != "private plan" || len(payloads) != 2 || strings.Contains(strings.Join(payloads, "\n"), `"reasoning":`) || !strings.Contains(strings.Join(payloads, "\n"), `"reasoning_content":`) {
		t.Fatalf("response=%+v payloads=%v err=%v", response, payloads, err)
	}
}

func TestGroqRejectsInvalidReasoningControlsBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewGroq(server.URL, "key", true)
	include := true
	tests := []openai.ChatGenerationOptions{
		{ReasoningFormat: "invalid"},
		{IncludeReasoning: &include, ReasoningFormat: "parsed"},
	}
	for _, options := range tests {
		_, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "qwen/qwen3.8-27b", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: options})
		if err == nil || called {
			t.Fatalf("options=%+v err=%v called=%v", options, err, called)
		}
	}
}

func TestGroqPreservesExplicitFalseIncludeReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatal("decode request")
		}
		value, found := body["include_reasoning"]
		if !found || value != false {
			t.Fatalf("include_reasoning=%#v found=%v", value, found)
		}
		_, _ = fmt.Fprint(w, `{"id":"chat","model":"model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	include := false
	_, err := NewGroq(server.URL, "key", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "openai/gpt-oss-20b", Messages: []openai.Message{{Role: "user", Content: "hello"}},
		ChatGenerationOptions: openai.ChatGenerationOptions{IncludeReasoning: &include},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGroqReasoningControlsAreModelScoped(t *testing.T) {
	client := NewGroq("http://unused.invalid", "", false)
	include := true
	tools := []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "lookup", Parameters: map[string]any{"type": "object"}}}}
	tests := []struct {
		name, model, parameter string
		options                openai.ChatGenerationOptions
		tools                  []openai.Tool
		valid                  bool
	}{
		{name: "gpt low", model: "openai/gpt-oss-20b", options: openai.ChatGenerationOptions{ReasoningEffort: "low"}, valid: true},
		{name: "gpt high", model: "openai/gpt-oss-120b", options: openai.ChatGenerationOptions{ReasoningEffort: "high"}, valid: true},
		{name: "gpt none", model: "openai/gpt-oss-20b", parameter: "reasoning_effort", options: openai.ChatGenerationOptions{ReasoningEffort: "none"}},
		{name: "gpt format", model: "openai/gpt-oss-20b", parameter: "reasoning_format", options: openai.ChatGenerationOptions{ReasoningFormat: "parsed"}},
		{name: "gpt include", model: "openai/gpt-oss-20b", options: openai.ChatGenerationOptions{IncludeReasoning: &include}, valid: true},
		{name: "qwen none", model: "qwen/qwen3.8-27b", options: openai.ChatGenerationOptions{ReasoningEffort: "none"}, valid: true},
		{name: "qwen default", model: "qwen/qwen3.8-27b", options: openai.ChatGenerationOptions{ReasoningEffort: "default"}, valid: true},
		{name: "qwen parsed", model: "qwen/qwen3.8-27b", options: openai.ChatGenerationOptions{ReasoningFormat: "parsed"}, valid: true},
		{name: "qwen raw tools", model: "qwen/qwen3.8-27b", parameter: "reasoning_format", options: openai.ChatGenerationOptions{ReasoningFormat: "raw"}, tools: tools},
		{name: "qwen include", model: "qwen/qwen3.8-27b", parameter: "include_reasoning", options: openai.ChatGenerationOptions{IncludeReasoning: &include}},
		{name: "unknown effort", model: "other", parameter: "reasoning_effort", options: openai.ChatGenerationOptions{ReasoningEffort: "high"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := client.ValidateChatParameters(openai.ChatCompletionRequest{Model: test.model, Messages: []openai.Message{{Role: "user", Content: "hello"}}, Tools: test.tools, ChatGenerationOptions: test.options})
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var failure *Error
			if !errors.As(err, &failure) || failure.Param != test.parameter || failure.UpstreamCode != "invalid_request" {
				t.Fatalf("failure=%+v err=%v", failure, err)
			}
		})
	}
}

func TestGroqCapabilityProfilePublishesExactReasoningPolicies(t *testing.T) {
	var policies []ProviderChatModelParameterPolicy
	for _, profile := range ManagedProviderCapabilityProfiles() {
		if profile.Type == "groq" {
			if len(profile.ChatParameters.ReasoningEffort) != 0 || len(profile.ChatParameters.ReasoningFormat) != 0 || slicesContain(profile.ChatParameters.SupportedOptions, "include_reasoning") || slicesContain(profile.ChatParameters.SupportedOptions, "reasoning_format") {
				t.Fatalf("provider-wide reasoning policy=%+v", profile.ChatParameters)
			}
			policies = profile.ChatModelParameters
			break
		}
	}
	want := []ProviderChatModelParameterPolicy{
		{Model: "openai/gpt-oss-20b", SupportedOptions: []string{"include_reasoning", "reasoning_effort"}, ReasoningEffort: []string{"low", "medium", "high"}, ReasoningFormat: []string{}},
		{Model: "openai/gpt-oss-120b", SupportedOptions: []string{"include_reasoning", "reasoning_effort"}, ReasoningEffort: []string{"low", "medium", "high"}, ReasoningFormat: []string{}},
		{Model: "qwen/qwen3.8-27b", SupportedOptions: []string{"reasoning_effort", "reasoning_format"}, ReasoningEffort: []string{"none", "low", "medium", "high", "default"}, ReasoningFormat: []string{"hidden", "raw", "parsed"}},
	}
	if fmt.Sprint(policies) != fmt.Sprint(want) {
		t.Fatalf("policies=%+v want=%+v", policies, want)
	}
}

func TestGroqReasoningControlsAreAdapterIsolated(t *testing.T) {
	include := false
	for name, validate := range map[string]func(openai.ChatCompletionRequest) error{
		"compatible": NewOpenAICompatible("http://unused.invalid", "", false).ValidateChatParameters,
		"cerebras":   NewCerebras("http://unused.invalid", "", false).ValidateChatParameters,
	} {
		t.Run(name, func(t *testing.T) {
			for parameter, options := range map[string]openai.ChatGenerationOptions{
				"include_reasoning": {IncludeReasoning: &include},
				"reasoning_format":  {ReasoningFormat: "raw"},
			} {
				var failure *Error
				err := validate(openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: options})
				if !errors.As(err, &failure) || failure.Param != parameter || failure.UpstreamCode != "unsupported_parameter" {
					t.Fatalf("%s: %v", parameter, err)
				}
			}
		})
	}
}

func TestGroqResponsesMapsSupportedContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/responses" || r.Header.Get("Authorization") != "Bearer groq-key" {
			t.Fatalf("path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		reasoning, ok := body["reasoning"].(map[string]any)
		if !ok || reasoning["effort"] != "low" {
			t.Fatalf("reasoning=%#v", body["reasoning"])
		}
		tools, ok := body["tools"].([]any)
		if !ok || len(tools) != 2 || tools[1].(map[string]any)["type"] != "mcp" {
			t.Fatalf("tools=%#v", body["tools"])
		}
		if body["model"] != "model" || body["input"] != "hello" || body["instructions"] != "be brief" || body["max_output_tokens"] != float64(64) || body["service_tier"] != "flex" || body["user"] != "tenant-user" || body["store"] != false || body["parallel_tool_calls"] != true || body["metadata"].(map[string]any)["ticket"] != "42" || body["text"] == nil {
			t.Fatalf("request=%#v", body)
		}
		_, _ = fmt.Fprint(w, `{"id":"response","object":"response","status":"completed","model":"model","service_tier":"flex","output":[{"id":"message","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":2,"input_tokens_details":{"cached_tokens":1,"reasoning_tokens":1},"output_tokens":1,"output_tokens_details":{"cached_tokens":1,"reasoning_tokens":0},"total_tokens":3}}`)
	}))
	defer server.Close()

	maxTokens := 64
	parallel, store := true, false
	effort := "low"
	client := NewGroq(server.URL+"/openai/v1", "groq-key", true)
	response, err := client.Responses(t.Context(), openai.ResponseRequest{
		Model: "model", Input: "hello", Instructions: "be brief", MaxOutputTokens: &maxTokens,
		Metadata: map[string]string{"ticket": "42"}, ParallelToolCalls: &parallel,
		Reasoning: &openai.ResponseReasoning{Effort: &effort}, Store: &store, ServiceTier: "flex", User: "tenant-user",
		Text: map[string]any{"format": map[string]any{"type": "json_object"}},
		Tools: []openai.ResponseTool{
			{Type: "function", Name: "lookup", Parameters: map[string]any{"type": "object"}},
			{Type: "mcp", ServerLabel: "catalog", ServerURL: "https://mcp.example.test", RequireApproval: "never"},
		},
	})
	if err != nil || response.ServiceTier != "flex" || response.Usage.TotalTokens != 3 || response.OutputText != "ok" || response.Usage.InputTokensDetails == nil || response.Usage.InputTokensDetails.ReasoningTokens != 1 || response.Usage.OutputTokensDetails == nil || response.Usage.OutputTokensDetails.CachedTokens != 1 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestGroqStreamsResponsesWithUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["stream"] != true || body["service_tier"] != "default" {
			t.Fatalf("request=%#v", body)
		}
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"response\",\"object\":\"response\",\"model\":\"model\",\"service_tier\":\"default\",\"status\":\"completed\",\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"output_tokens_details\":{\"cached_tokens\":1},\"total_tokens\":3}}}\n\n")
	}))
	defer server.Close()

	client := NewGroq(server.URL, "key", true)
	var events []string
	response, err := client.StreamResponses(t.Context(), openai.ResponseRequest{Model: "model", Input: "hello", ServiceTier: "default", Stream: true}, func(_ string, payload string) error {
		events = append(events, payload)
		return nil
	})
	if err != nil || response.ServiceTier != "default" || response.Usage.TotalTokens != 3 || response.OutputText != "ok" || response.Usage.OutputTokensDetails == nil || response.Usage.OutputTokensDetails.CachedTokens != 1 || len(events) != 2 {
		t.Fatalf("response=%+v events=%v err=%v", response, events, err)
	}
}

func TestGroqResponsesRejectUnsupportedParametersBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewGroq(server.URL, "key", true)

	store := true
	truncation := "auto"
	topLogprobs, maxToolCalls := 1, 1
	penalty := 0.5
	reasoningSummary, invalidEffort := "auto", "max"
	tests := []struct {
		name, param, code string
		request           openai.ResponseRequest
	}{
		{name: "include", param: "include", code: "unsupported_parameter", request: openai.ResponseRequest{Include: []string{"reasoning.encrypted_content"}}},
		{name: "store", param: "store", code: "unsupported_parameter", request: openai.ResponseRequest{Store: &store}},
		{name: "truncation", param: "truncation", code: "unsupported_parameter", request: openai.ResponseRequest{Truncation: &truncation}},
		{name: "continuity", param: "previous_response_id", code: "unsupported_parameter", request: openai.ResponseRequest{PreviousResponse: "response"}},
		{name: "safety identifier", param: "safety_identifier", code: "unsupported_parameter", request: openai.ResponseRequest{SafetyIdentifier: "user"}},
		{name: "prompt cache key", param: "prompt_cache_key", code: "unsupported_parameter", request: openai.ResponseRequest{PromptCacheKey: "cache"}},
		{name: "text verbosity", param: "text.verbosity", code: "unsupported_parameter", request: openai.ResponseRequest{Text: map[string]any{"verbosity": "low"}}},
		{name: "top logprobs", param: "top_logprobs", code: "unsupported_parameter", request: openai.ResponseRequest{TopLogprobs: &topLogprobs}},
		{name: "frequency penalty", param: "frequency_penalty", code: "unsupported_parameter", request: openai.ResponseRequest{FrequencyPenalty: &penalty}},
		{name: "presence penalty", param: "presence_penalty", code: "unsupported_parameter", request: openai.ResponseRequest{PresencePenalty: &penalty}},
		{name: "max tool calls", param: "max_tool_calls", code: "unsupported_parameter", request: openai.ResponseRequest{MaxToolCalls: &maxToolCalls}},
		{name: "reasoning summary", param: "reasoning", code: "unsupported_parameter", request: openai.ResponseRequest{Reasoning: &openai.ResponseReasoning{Summary: &reasoningSummary}}},
		{name: "reasoning effort", param: "reasoning.effort", code: "invalid_request", request: openai.ResponseRequest{Reasoning: &openai.ResponseReasoning{Effort: &invalidEffort}}},
		{name: "service tier", param: "service_tier", code: "unsupported_parameter", request: openai.ResponseRequest{ServiceTier: "performance"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.request.Model = "model"
			test.request.Input = "hello"
			_, err := client.Responses(t.Context(), test.request)
			var failure *Error
			if !errors.As(err, &failure) || failure.Provider != "groq" || failure.Param != test.param || failure.UpstreamCode != test.code || called {
				t.Fatalf("failure=%+v err=%v called=%v", failure, err, called)
			}
		})
	}
}
