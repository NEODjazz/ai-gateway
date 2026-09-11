package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

var _ Client = XAI{}
var _ StreamingClient = XAI{}
var _ responseRetrieveClient = XAI{}
var _ responseInputItemsClient = XAI{}
var _ responseDeleteClient = XAI{}
var _ ResponseCompactClient = XAI{}

func TestXAIChatAndResponsesContracts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xai-key" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "grok" || body["service_tier"] != "priority" {
			t.Fatalf("request=%#v", body)
		}
		switch r.URL.Path {
		case "/v1/chat/completions":
			if body["reasoning_effort"] != "xhigh" || body["logprobs"] != true {
				t.Fatalf("chat request=%#v", body)
			}
			_, _ = fmt.Fprint(w, `{"id":"chat","object":"chat.completion","model":"grok","service_tier":"priority","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
		case "/v1/responses":
			reasoning, ok := body["reasoning"].(map[string]any)
			if !ok || reasoning["effort"] != "high" || body["prompt_cache_key"] != "conversation" {
				t.Fatalf("response request=%#v", body)
			}
			_, _ = fmt.Fprint(w, `{"id":"resp","object":"response","model":"grok","status":"completed","output":[{"id":"message","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
		default:
			t.Fatalf("path=%q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewXAI(server.URL+"/v1", "xai-key", true)
	logprobs := true
	chat, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "grok", Messages: []openai.Message{{Role: "user", Content: "hello"}},
		ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: "xhigh", Logprobs: &logprobs, ServiceTier: "priority"},
	})
	if err != nil || chat.Usage.TotalTokens != 3 || chat.ServiceTier != "priority" {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	effort := "high"
	response, err := client.Responses(t.Context(), openai.ResponseRequest{
		Model: "grok", Input: "hello", ServiceTier: "priority", PromptCacheKey: "conversation",
		Reasoning: &openai.ResponseReasoning{Effort: &effort},
	})
	if err != nil || response.Usage.TotalTokens != 3 || response.OutputText != "ok" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestXAIRejectsUnsupportedParametersBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewXAI(server.URL, "key", true)

	invalidChat := openai.ChatCompletionRequest{Model: "grok", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{ServiceTier: "auto"}}
	if _, err := client.ChatCompletions(t.Context(), invalidChat); !xaiFailure(err, "service_tier", "invalid_request") || called {
		t.Fatalf("chat err=%v called=%v", err, called)
	}
	penalty := 0.5
	topLogprobs := 8
	invalidEffort := "max"
	metadata := map[string]string{"ticket": "42"}
	truncation := "auto"
	maxToolCalls := 3
	tests := []struct {
		param string
		code  string
		req   openai.ResponseRequest
	}{
		{param: "background", code: "unsupported_parameter", req: openai.ResponseRequest{Background: true}},
		{param: "frequency_penalty", code: "unsupported_parameter", req: openai.ResponseRequest{FrequencyPenalty: &penalty}},
		{param: "presence_penalty", code: "unsupported_parameter", req: openai.ResponseRequest{PresencePenalty: &penalty}},
		{param: "top_logprobs", code: "unsupported_parameter", req: openai.ResponseRequest{TopLogprobs: &topLogprobs}},
		{param: "reasoning.effort", code: "invalid_request", req: openai.ResponseRequest{Reasoning: &openai.ResponseReasoning{Effort: &invalidEffort}}},
		{param: "service_tier", code: "invalid_request", req: openai.ResponseRequest{ServiceTier: "flex"}},
		{param: "metadata", code: "unsupported_parameter", req: openai.ResponseRequest{Metadata: metadata}},
		{param: "truncation", code: "unsupported_parameter", req: openai.ResponseRequest{Truncation: &truncation}},
		{param: "safety_identifier", code: "unsupported_parameter", req: openai.ResponseRequest{SafetyIdentifier: "user"}},
		{param: "max_tool_calls", code: "unsupported_parameter", req: openai.ResponseRequest{MaxToolCalls: &maxToolCalls}},
		{param: "text.verbosity", code: "unsupported_parameter", req: openai.ResponseRequest{Text: map[string]any{"verbosity": "high"}}},
	}
	for _, test := range tests {
		t.Run(test.param, func(t *testing.T) {
			test.req.Model = "grok"
			test.req.Input = "hello"
			_, err := client.Responses(t.Context(), test.req)
			if !xaiFailure(err, test.param, test.code) || called {
				t.Fatalf("err=%v called=%v", err, called)
			}
		})
	}
}

func TestXAIChatParameterPolicy(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewXAI(server.URL, "key", true)
	logprobs := true
	falseLogprobs := false
	topEight := 8
	topNine := 9
	if err := client.ValidateChatParameters(openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: "none", Logprobs: &logprobs, TopLogprobs: &topEight}}); err != nil {
		t.Fatalf("valid parameters: %v", err)
	}
	tests := []struct {
		name    string
		options openai.ChatGenerationOptions
		param   string
		code    string
	}{
		{name: "top logprobs limit", options: openai.ChatGenerationOptions{Logprobs: &logprobs, TopLogprobs: &topNine}, param: "top_logprobs", code: "invalid_request"},
		{name: "top logprobs requires logprobs", options: openai.ChatGenerationOptions{Logprobs: &falseLogprobs, TopLogprobs: &topEight}, param: "top_logprobs", code: "invalid_request"},
		{name: "logit bias", options: openai.ChatGenerationOptions{LogitBias: map[string]int{"1": 2}}, param: "logit_bias", code: "unsupported_parameter"},
		{name: "web fetch", options: openai.ChatGenerationOptions{WebFetchOptions: &openai.ChatWebFetchOptions{AllowedDomains: []string{"example.com"}, MaxContentTokens: 10}}, param: "web_fetch_options", code: "unsupported_parameter"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := openai.ChatCompletionRequest{Model: "grok", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: test.options}
			_, err := client.ChatCompletions(t.Context(), request)
			if !xaiFailure(err, test.param, test.code) || called {
				t.Fatalf("err=%v called=%v", err, called)
			}
		})
	}
}

func TestXAIResponseResourceLifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xai-key" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/responses/resp_1":
			_, _ = fmt.Fprint(w, `{"id":"resp_1","object":"response","model":"grok","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/responses/resp_1/input_items":
			if r.URL.Query().Get("after") != "item_1" || r.URL.Query().Get("limit") != "1" || r.URL.Query().Get("order") != "asc" {
				t.Fatalf("query=%v", r.URL.Query())
			}
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"item_2","type":"message"}],"has_more":false}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/responses/resp_1":
			_, _ = fmt.Fprint(w, `{"id":"resp_1","object":"response","deleted":true}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/responses/compact":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["model"] != "grok" || body["input"] != "history" {
				t.Fatalf("compact body=%#v err=%v", body, err)
			}
			_, _ = fmt.Fprint(w, `{"id":"cmp_1","object":"response.compaction","created_at":1,"output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()
	client := NewXAI(server.URL+"/v1", "xai-key", true)
	response, err := client.RetrieveResponse(t.Context(), "resp_1")
	if err != nil || response.ID != "resp_1" || response.Usage.TotalTokens != 3 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	items, err := client.ListResponseInputItems(t.Context(), "resp_1", ResponseInputItemsOptions{After: "item_1", Limit: 1, Order: "asc"})
	if err != nil || len(items.Data) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	deleted, err := client.DeleteResponse(t.Context(), "resp_1")
	if err != nil || !deleted.Deleted || deleted.Object != "response" {
		t.Fatalf("deleted=%+v err=%v", deleted, err)
	}
	compacted, err := client.CompactResponse(t.Context(), openai.ResponseCompactRequest{Model: "grok", Input: "history"})
	if err != nil || compacted.ID != "cmp_1" || compacted.Usage.TotalTokens != 12 {
		t.Fatalf("compacted=%+v err=%v", compacted, err)
	}
}

func xaiFailure(err error, param, code string) bool {
	var failure *Error
	return errors.As(err, &failure) && failure.Provider == "xai" && failure.Param == param && failure.UpstreamCode == code
}
