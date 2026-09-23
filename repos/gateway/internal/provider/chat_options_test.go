package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestCompatibleChatGenerationOptionsRoundTrip(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			var received map[string]json.RawMessage
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Error(err)
					return
				}
				if streaming {
					for _, token := range []string{"one", "two"} {
						_, _ = fmt.Fprintf(w, "data: {\"id\":\"chat-test\",\"created\":123,\"model\":\"test\",\"metadata\":{\"trace\":\"one\"},\"service_tier\":\"priority\",\"system_fingerprint\":\"fp-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"logprobs\":{\"content\":[{\"token\":%q,\"logprob\":-0.5,\"bytes\":[1],\"top_logprobs\":[]}]}},{\"index\":1,\"delta\":{\"content\":%q}}]}\n\n", token, token, token)
					}
					_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
				} else {
					_, _ = fmt.Fprint(w, `{"id":"chat-test","object":"chat.completion","created":123,"model":"test","metadata":{"trace":"one"},"service_tier":"priority","system_fingerprint":"fp-test","choices":[{"index":0,"message":{"role":"assistant","content":"one"},"logprobs":{"content":[{"token":"one","logprob":-0.5,"bytes":[1],"top_logprobs":[]}]}},{"index":1,"message":{"role":"assistant","content":"two"}}]}`)
				}
			}))
			defer server.Close()
			var request openai.ChatCompletionRequest
			if err := json.Unmarshal([]byte(`{"model":"test","messages":[{"role":"user","content":[{"type":"text","text":"hello","prompt_cache_breakpoint":{"mode":"explicit"}}]}],"stream_options":{"include_usage":false,"include_obfuscation":false},"metadata":{"trace":"one"},"store":false,"modalities":["text"],"reasoning_effort":"high","n":2,"safety_identifier":"hashed-user","prompt_cache_key":"tenant-thread","prompt_cache_options":{"mode":"explicit","ttl":"30m"},"prompt_cache_retention":"24h","prediction":{"type":"content","content":"expected"},"user":"legacy-user","verbosity":"low","web_search_options":{"search_context_size":"high","user_location":{"type":"approximate","approximate":{"country":"FR"}}},"logprobs":true,"top_logprobs":0,"frequency_penalty":0,"presence_penalty":-1,"min_p":0.05,"top_k":40,"top_a":0.2,"repetition_penalty":1.1,"logit_bias":{"10":-100}}`), &request); err != nil {
				t.Fatal(err)
			}
			client := NewOpenAICompatible(server.URL, "", true)
			var response openai.ChatCompletionResponse
			var err error
			var payloads []string
			if streaming {
				response, err = client.StreamChatCompletions(context.Background(), request, func(s string) error { payloads = append(payloads, s); return nil })
			} else {
				response, err = client.ChatCompletions(context.Background(), request)
			}
			if err != nil {
				t.Fatal(err)
			}
			for name, want := range map[string]string{"metadata": `{"trace":"one"}`, "store": "false", "modalities": `["text"]`, "reasoning_effort": `"high"`, "n": "2", "safety_identifier": `"hashed-user"`, "prompt_cache_key": `"tenant-thread"`, "prompt_cache_options": `{"mode":"explicit","ttl":"30m"}`, "prompt_cache_retention": `"24h"`, "prediction": `{"content":"expected","type":"content"}`, "user": `"legacy-user"`, "verbosity": `"low"`, "web_search_options": `{"search_context_size":"high","user_location":{"type":"approximate","approximate":{"country":"FR"}}}`, "logprobs": "true", "top_logprobs": "0", "frequency_penalty": "0", "presence_penalty": "-1", "min_p": "0.05", "top_k": "40", "top_a": "0.2", "repetition_penalty": "1.1", "logit_bias": `{"10":-100}`} {
				if string(received[name]) != want {
					t.Fatalf("%s=%s, want %s", name, received[name], want)
				}
			}
			if !strings.Contains(string(received["messages"]), `"prompt_cache_breakpoint":{"mode":"explicit"}`) {
				t.Fatalf("prompt cache breakpoint was not forwarded: %s", received["messages"])
			}
			if streaming {
				if string(received["stream_options"]) != `{"include_usage":true,"include_obfuscation":false}` {
					t.Fatalf("stream usage was not requested: %s", received["stream_options"])
				}
			} else if _, present := received["stream_options"]; present {
				t.Fatal("stream_options sent for a non-streaming request")
			}
			wantCount := 1
			if streaming {
				wantCount = 2
				if len(payloads) != 2 || !strings.Contains(payloads[0], "logprobs") {
					t.Fatal("SSE logprobs lost")
				}
			}
			if len(response.Choices) != 2 || response.Choices[0].Logprobs == nil || len(response.Choices[0].Logprobs.Content) != wantCount {
				t.Fatalf("response logprobs lost: %+v", response)
			}
			if response.ID != "chat-test" || response.Created != 123 || response.Model != "test" || response.Metadata["trace"] != "one" || response.ServiceTier != "priority" || response.SystemFingerprint != "fp-test" {
				t.Fatalf("response envelope lost: %+v", response)
			}
		})
	}
}

func TestCompatibleChatRefusalRoundTrip(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			var received openai.ChatCompletionRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Error(err)
					return
				}
				if streaming {
					_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-refusal\",\"created\":1,\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"refusal\":\"cannot \"}}]}\n\n")
					_, _ = fmt.Fprint(w, "data: {\"id\":\"chat-refusal\",\"created\":1,\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"refusal\":\"help\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					return
				}
				_, _ = fmt.Fprint(w, `{"id":"chat-refusal","object":"chat.completion","created":1,"model":"test","choices":[{"index":0,"message":{"role":"assistant","content":null,"refusal":"cannot help"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			}))
			defer server.Close()
			historical := "previous refusal"
			request := openai.ChatCompletionRequest{Model: "test", Stream: streaming, Messages: []openai.Message{{Role: "assistant", Refusal: &historical}}}
			client := NewOpenAICompatible(server.URL, "", true)
			var response openai.ChatCompletionResponse
			var err error
			if streaming {
				response, err = client.StreamChatCompletions(t.Context(), request, func(string) error { return nil })
			} else {
				response, err = client.ChatCompletions(t.Context(), request)
			}
			if err != nil || len(received.Messages) != 1 || received.Messages[0].Refusal == nil || *received.Messages[0].Refusal != historical || response.Choices[0].Message.Refusal == nil || *response.Choices[0].Message.Refusal != "cannot help" {
				t.Fatalf("refusal was not preserved: received=%+v response=%+v err=%v", received.Messages, response, err)
			}
		})
	}
}

func TestCompatibleChatPreservesURLCitations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"chat-citation","object":"chat.completion","created":1,"model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"Source","annotations":[{"type":"url_citation","url_citation":{"start_index":0,"end_index":6,"title":"Example","url":"https://example.com/source"}}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	response, err := NewOpenAICompatible(server.URL, "", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	annotations := response.Choices[0].Message.Annotations
	if len(annotations) != 1 || annotations[0].Type != "url_citation" || annotations[0].URLCitation.URL != "https://example.com/source" || annotations[0].URLCitation.EndIndex != 6 {
		t.Fatalf("URL citation was not preserved: %+v", annotations)
	}
	payload, err := json.Marshal(response)
	if err != nil || !strings.Contains(string(payload), `"annotations":[{"type":"url_citation"`) {
		t.Fatalf("URL citation was not serialized: payload=%s err=%v", payload, err)
	}
}

func TestCompatibleChatRejectsInvalidURLCitations(t *testing.T) {
	for name, citation := range map[string]string{
		"unsafe URL":      `{"start_index":0,"end_index":6,"title":"Example","url":"javascript:alert(1)"}`,
		"reversed offset": `{"start_index":7,"end_index":6,"title":"Example","url":"https://example.com/source"}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"Source","annotations":[{"type":"url_citation","url_citation":%s}]}}]}`, citation)
			}))
			defer server.Close()
			if _, err := NewOpenAICompatible(server.URL, "", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "test"}); err == nil || !strings.Contains(err.Error(), "annotations") {
				t.Fatalf("invalid URL citation accepted: %v", err)
			}
		})
	}
}

func TestChatRefusalHistoryIsRejectedByNativeAdapters(t *testing.T) {
	refusal := "cannot help"
	request := openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "assistant", Refusal: &refusal}}}
	for _, client := range []Client{NewAnthropic("http://unused.invalid", "", false), NewOllama("http://unused.invalid", false), NewGemini("http://unused.invalid", "", false), Demo{}} {
		var failure *Error
		err := validateChatAdapter(client, request)
		if !errors.As(err, &failure) || failure.Param != "messages.refusal" || failure.UpstreamCode != "unsupported_parameter" {
			t.Fatalf("%T silently accepted refusal history: %v", client, err)
		}
	}
}

func TestChatRefusalHistoryScopesExactCache(t *testing.T) {
	base := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "assistant", Content: nil}}}}
	changed := base
	refusal := "cannot help"
	changed.Request.Messages = []openai.Message{{Role: "assistant", Content: nil, Refusal: &refusal}}
	if providerCacheKey("chat", base) == providerCacheKey("chat", changed) {
		t.Fatal("exact cache ignored refusal history")
	}
}

func TestPromptCacheBreakpointsAreRejectedByUnsupportedNativeAdapters(t *testing.T) {
	var request openai.ChatCompletionRequest
	if err := json.Unmarshal([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello","prompt_cache_breakpoint":{"mode":"explicit"}}]}]}`), &request); err != nil {
		t.Fatal(err)
	}
	if err := validateChatAdapter(NewAnthropic("http://unused.invalid", "", false), request); err != nil {
		t.Fatalf("Anthropic rejected prompt cache breakpoint: %v", err)
	}
	for _, client := range []Client{NewOllama("http://unused.invalid", false), NewGemini("http://unused.invalid", "", false), Demo{}} {
		var failure *Error
		err := validateChatAdapter(client, request)
		if !errors.As(err, &failure) || failure.Param != "messages.prompt_cache_breakpoint" || failure.UpstreamCode != "unsupported_parameter" {
			t.Fatalf("%T silently accepted breakpoint: %v", client, err)
		}
	}
}

func TestAnthropicRejectsCachedToolWhenToolChoiceIsNone(t *testing.T) {
	request := openai.ChatCompletionRequest{
		Messages:   []openai.Message{{Role: "user", Content: "hello"}},
		Tools:      []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "lookup", Parameters: map[string]any{"type": "object"}, PromptCacheBreakpoint: &openai.PromptCacheBreakpoint{Mode: "explicit"}}}},
		ToolChoice: "none",
	}
	err := (Anthropic{}).ValidateChatParameters(request)
	var failure *Error
	if !errors.As(err, &failure) || failure.Param != "tool_choice" || failure.UpstreamCode != "invalid_request" {
		t.Fatalf("cached tool was silently removed: %v", err)
	}
}

func TestCompatibleChatRejectsIncompleteMultiChoiceResponse(t *testing.T) {
	choices := 2
	request := openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{N: &choices}, Model: "test"}
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if streaming {
					_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"one\"}}]}\n\ndata: [DONE]\n\n")
					return
				}
				_, _ = fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"one"}}]}`)
			}))
			defer server.Close()
			client := NewOpenAICompatible(server.URL, "", streaming)
			var err error
			if streaming {
				_, err = client.StreamChatCompletions(t.Context(), request, func(string) error { return nil })
			} else {
				_, err = client.ChatCompletions(t.Context(), request)
			}
			if err == nil || !strings.Contains(err.Error(), "choice count") {
				t.Fatalf("incomplete multi-choice response accepted: %v", err)
			}
		})
	}
}

func TestRequestedChatChoiceIndicesAreExact(t *testing.T) {
	choices := 2
	request := openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{N: &choices}}
	if err := validateRequestedChatChoices(request, openai.ChatCompletionResponse{Choices: []openai.Choice{{Index: 1}, {Index: 0}}}); err != nil {
		t.Fatal(err)
	}
	for _, response := range []openai.ChatCompletionResponse{
		{Choices: []openai.Choice{{Index: 0}}},
		{Choices: []openai.Choice{{Index: 0}, {Index: 0}}},
		{Choices: []openai.Choice{{Index: 0}, {Index: 2}}},
	} {
		if err := validateRequestedChatChoices(request, response); err == nil {
			t.Fatalf("invalid choices accepted: %+v", response.Choices)
		}
	}
}

func TestChatCompletionJSONResponseIsBoundedAndExact(t *testing.T) {
	var response openai.ChatCompletionResponse
	if err := decodeChatCompletionResponse(strings.NewReader(`{"choices":[]} {}`), &response); err == nil {
		t.Fatal("trailing chat completion JSON accepted")
	}
	reader := &embeddingLimitReader{}
	if err := decodeChatCompletionResponse(reader, &response); err == nil || reader.read != maxChatCompletionResponseBytes+1 {
		t.Fatalf("unbounded chat completion response: bytes=%d err=%v", reader.read, err)
	}
}

func TestCompatibleChatRejectsInvalidResponseEnvelope(t *testing.T) {
	for name, payload := range map[string]string{
		"negative timestamp":   `{"created":-1,"choices":[]}`,
		"oversized metadata":   `{"metadata":{"trace":"` + strings.Repeat("x", 513) + `"},"choices":[]}`,
		"unknown service tier": `{"service_tier":"unknown","choices":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, payload)
			}))
			defer server.Close()
			if _, err := NewOpenAICompatible(server.URL, "", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "test"}); err == nil {
				t.Fatal("invalid response envelope accepted")
			}
		})
	}
}

func TestCompatibleChatRejectsInvalidUsageBeforeDelivery(t *testing.T) {
	for _, invalid := range []string{
		`{"prompt_tokens":2,"completion_tokens":3,"total_tokens":4}`,
		`{"prompt_tokens_details":{"audio_tokens":-1}}`,
		`{"prompt_tokens_details":{"image_tokens":-1}}`,
		`{"prompt_tokens_details":{"text_tokens":-1}}`,
		`{"completion_tokens_details":{"accepted_prediction_tokens":-1}}`,
		`{"completion_tokens_details":{"audio_tokens":-1}}`,
		`{"completion_tokens_details":{"rejected_prediction_tokens":-1}}`,
		`{"completion_tokens_details":{"text_tokens":-1}}`,
	} {
		t.Run(invalid, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"one"}}],"usage":`+invalid+`}`)
			}))
			defer server.Close()
			if _, err := NewOpenAICompatible(server.URL, "", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "test"}); err == nil {
				t.Fatal("invalid JSON usage accepted")
			}
			wrote := false
			payload := `data: {"choices":[],"usage":` + invalid + `}` + "\n\n"
			if _, err := streamChatCompletionData(strings.NewReader(payload), "test", func(string) error { wrote = true; return nil }); err == nil || wrote {
				t.Fatalf("invalid SSE usage delivered: err=%v wrote=%v", err, wrote)
			}
		})
	}
}

func TestGenerationControlsAreRejectedByNativeAdapters(t *testing.T) {
	for _, body := range []string{`{"metadata":{"trace":"one"}}`, `{"store":false}`, `{"modalities":["text"]}`, `{"thinking":{"type":"enabled"}}`, `{"reasoning_effort":"high"}`, `{"n":2}`, `{"safety_identifier":"hashed-user"}`, `{"prompt_cache_key":"tenant-thread"}`, `{"prompt_cache_options":{"mode":"explicit"}}`, `{"prompt_cache_retention":"24h"}`, `{"prediction":{"type":"content","content":"expected"}}`, `{"service_tier":"priority"}`, `{"user":"legacy-user"}`, `{"verbosity":"low"}`, `{"web_search_options":{}}`, `{"logprobs":false}`, `{"top_logprobs":0}`, `{"frequency_penalty":0}`, `{"presence_penalty":0}`, `{"min_p":0}`, `{"top_k":0}`, `{"top_a":0}`, `{"repetition_penalty":1}`, `{"logit_bias":{"1":0}}`} {
		var request openai.ChatCompletionRequest
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			t.Fatal(err)
		}
		for _, client := range []Client{NewAnthropic("http://unused.invalid", "", false), NewOllama("http://unused.invalid", false), Demo{}} {
			if _, anthropic := client.(Anthropic); anthropic && (request.ReasoningEffort == "high" || request.WebSearchOptions != nil || request.TopK != nil) {
				continue
			}
			if _, ollama := client.(Ollama); ollama && (request.MinP != nil || request.TopK != nil || request.Logprobs != nil || request.TopLogprobs != nil || request.ReasoningEffort == "none" || request.ReasoningEffort == "high") {
				continue
			}
			if err := validateChatAdapter(client, request); err == nil {
				t.Fatalf("%T silently accepted %s", client, body)
			}
		}
	}
}

func TestExtendedSamplingControlsAreRejectedBySpecializedAdapters(t *testing.T) {
	adapters := []struct {
		name     string
		validate func(openai.ChatCompletionRequest) error
	}{
		{"mistral", NewMistral("http://unused.invalid", "", false).ValidateChatParameters},
		{"cohere", NewCohere("http://unused.invalid", "").ValidateChatParameters},
		{"groq", NewGroq("http://unused.invalid", "", false).ValidateChatParameters},
		{"deepseek", NewDeepSeek("http://unused.invalid", "", false).ValidateChatParameters},
		{"bedrock", NewBedrock("http://unused.invalid", "").ValidateChatParameters},
	}
	for _, body := range []string{`{"min_p":0}`, `{"top_k":0}`, `{"top_a":0}`, `{"repetition_penalty":1}`} {
		var request openai.ChatCompletionRequest
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			t.Fatal(err)
		}
		request.Model = "test"
		request.Messages = []openai.Message{{Role: "user", Content: "hello"}}
		for _, adapter := range adapters {
			t.Run(adapter.name+body, func(t *testing.T) {
				if adapter.name == "cohere" && (request.TopK != nil || request.Logprobs != nil) {
					return
				}
				var failure *Error
				if err := adapter.validate(request); !errors.As(err, &failure) || failure.UpstreamCode != "unsupported_parameter" {
					t.Fatalf("sampling control was not rejected explicitly: %v", err)
				}
			})
		}
	}
}

func TestExtendedSamplingControlsScopeExactCache(t *testing.T) {
	base := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "hello"}}}}
	minP := 0.05
	changed := base
	changed.Request.MinP = &minP
	if providerCacheKey("chat", base) == providerCacheKey("chat", changed) {
		t.Fatal("exact cache ignored min_p")
	}
}

func TestChatModalitiesAreRejectedByUnsupportedNativeAdapters(t *testing.T) {
	request := openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{Modalities: []string{"text"}}}
	for _, client := range []Client{NewAnthropic("http://unused.invalid", "", false), NewOllama("http://unused.invalid", false), Demo{}} {
		var failure *Error
		err := validateChatAdapter(client, request)
		if !errors.As(err, &failure) || failure.Param != "modalities" || failure.UpstreamCode != "unsupported_parameter" {
			t.Fatalf("%T silently accepted modalities: %v", client, err)
		}
	}
}

func TestCompatibleChatRejectsStoredRequests(t *testing.T) {
	store := true
	err := NewOpenAICompatible("http://unused.invalid", "", false).ValidateChatParameters(openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{Store: &store}})
	var failure *Error
	if !errors.As(err, &failure) || failure.Param != "store" || failure.UpstreamCode != "unsupported_parameter" {
		t.Fatalf("stored Chat request was not rejected explicitly: %v", err)
	}
}

func TestLogprobsDisableSemanticCacheAndScopeExactCache(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "hello"}}}}
	before := providerCacheKey("chat", request)
	enabled := true
	request.Request.Logprobs = &enabled
	if _, _, ok := semanticRequest(request, Endpoint{Name: "test"}); ok {
		t.Fatal("semantic reuse of log probabilities allowed")
	}
	if providerCacheKey("chat", request) == before {
		t.Fatal("exact cache ignored logprobs")
	}
}

func TestReasoningContentScopesExactCacheAndDisablesSemanticCache(t *testing.T) {
	base := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "hello"}}}}
	changed := base
	changed.Request.Messages = append([]openai.Message(nil), base.Request.Messages...)
	changed.Request.Messages = append(changed.Request.Messages, openai.Message{Role: "assistant", Content: "answer", ReasoningContent: "plan"})
	if providerCacheKey("chat", base) == providerCacheKey("chat", changed) {
		t.Fatal("exact cache ignored reasoning_content")
	}
	if _, _, ok := semanticRequest(changed, Endpoint{Name: "test"}); ok {
		t.Fatal("semantic cache accepted reasoning_content")
	}
}

func TestSafetyIdentifierScopesCaches(t *testing.T) {
	base := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "hello"}}}}
	changed := base
	changed.Request.SafetyIdentifier = "hashed-user"
	if providerCacheKey("chat", base) == providerCacheKey("chat", changed) {
		t.Fatal("exact cache ignored safety_identifier")
	}
	baseScope, _, baseOK := semanticRequest(base, Endpoint{Name: "test"})
	changedScope, _, changedOK := semanticRequest(changed, Endpoint{Name: "test"})
	if !baseOK || !changedOK || baseScope == changedScope {
		t.Fatal("semantic cache ignored safety_identifier")
	}
}

func TestChatUserScopesCaches(t *testing.T) {
	base := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "hello"}}}}
	changed := base
	changed.Request.User = "legacy-user"
	if providerCacheKey("chat", base) == providerCacheKey("chat", changed) {
		t.Fatal("exact cache ignored user")
	}
	baseScope, _, baseOK := semanticRequest(base, Endpoint{Name: "test"})
	changedScope, _, changedOK := semanticRequest(changed, Endpoint{Name: "test"})
	if !baseOK || !changedOK || baseScope == changedScope {
		t.Fatal("semantic cache ignored user")
	}
}

func TestChatWebSearchDisablesResponseCaches(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "latest news"}}, ChatGenerationOptions: openai.ChatGenerationOptions{WebSearchOptions: &openai.ChatWebSearchOptions{SearchContextSize: "high"}}}}
	if providerCacheKey("chat", request) != "" {
		t.Fatal("exact cache allowed a web search request")
	}
	if _, _, ok := semanticRequest(request, Endpoint{Name: "test"}); ok {
		t.Fatal("semantic cache allowed a web search request")
	}
	if got := strings.Join(requiredChatCapabilities(request.Request, true), ","); got != "chat,stream,web_search" {
		t.Fatalf("web search routing requirements=%s", got)
	}
}

func TestGeminiSearchTimeRangeRequiresNativeCapability(t *testing.T) {
	request := openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "news"}}, ChatGenerationOptions: openai.ChatGenerationOptions{WebSearchOptions: &openai.ChatWebSearchOptions{GeminiTimeRange: &openai.GeminiSearchTimeRange{StartTime: "2026-01-01T00:00:00Z", EndTime: "2026-02-01T00:00:00Z"}}}}
	if got := strings.Join(requiredChatCapabilities(request, false), ","); got != "chat,web_search,gemini_search_time_range" {
		t.Fatalf("search time-range routing requirements=%s", got)
	}
}

func TestGeminiFileSearchDisablesResponseCaches(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "find policy"}}, GeminiFileSearch: &openai.GeminiFileSearchConfig{StoreNames: []string{"fileSearchStores/policies"}}}}
	if providerCacheKey("chat", request) != "" {
		t.Fatal("exact cache allowed file search")
	}
	if _, _, ok := semanticRequest(request, Endpoint{Name: "test"}); ok {
		t.Fatal("semantic cache allowed file search")
	}
	if got := strings.Join(requiredChatCapabilities(request.Request, false), ","); got != "chat,gemini_file_search" {
		t.Fatalf("file search routing requirements=%s", got)
	}
}

func TestGeminiComputerUseDisablesResponseCaches(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "browse"}}, GeminiComputerUse: &openai.GeminiComputerUseConfig{Environment: "ENVIRONMENT_BROWSER"}}}
	if providerCacheKey("chat", request) != "" {
		t.Fatal("exact cache allowed computer use")
	}
	if _, _, ok := semanticRequest(request, Endpoint{Name: "test"}); ok {
		t.Fatal("semantic cache allowed computer use")
	}
	if got := strings.Join(requiredChatCapabilities(request.Request, false), ","); got != "chat,gemini_computer_use" {
		t.Fatalf("computer use routing requirements=%s", got)
	}
}

func TestGeminiMCPDisablesResponseCaches(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "forecast"}}, GeminiMCPServerIDs: []string{"weather"}}}
	if providerCacheKey("chat", request) != "" {
		t.Fatal("exact cache allowed MCP")
	}
	if _, _, ok := semanticRequest(request, Endpoint{Name: "test"}); ok {
		t.Fatal("semantic cache allowed MCP")
	}
	if got := strings.Join(requiredChatCapabilities(request.Request, false), ","); got != "chat,gemini_mcp" {
		t.Fatalf("MCP routing requirements=%s", got)
	}
}

func TestGeminiCodeExecutionDisablesResponseCaches(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "calculate"}}, GeminiCodeExecution: true}}
	if providerCacheKey("chat", request) != "" {
		t.Fatal("exact cache allowed a code execution request")
	}
	if _, _, ok := semanticRequest(request, Endpoint{Name: "test"}); ok {
		t.Fatal("semantic cache allowed a code execution request")
	}
	if got := strings.Join(requiredChatCapabilities(request.Request, false), ","); got != "chat,gemini_code_execution" {
		t.Fatalf("code execution routing requirements=%s", got)
	}
}

func TestGeminiURLContextDisablesResponseCaches(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "summarize https://example.com"}}, GeminiURLContext: true}}
	if providerCacheKey("chat", request) != "" {
		t.Fatal("exact cache allowed a URL context request")
	}
	if _, _, ok := semanticRequest(request, Endpoint{Name: "test"}); ok {
		t.Fatal("semantic cache allowed a URL context request")
	}
	if got := strings.Join(requiredChatCapabilities(request.Request, false), ","); got != "chat,url_context" {
		t.Fatalf("URL context routing requirements=%s", got)
	}
}

func TestGeminiGoogleMapsDisablesResponseCaches(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "restaurants near here"}}, GeminiGoogleMaps: true, GeminiRetrievalLocation: &openai.GeminiLatLng{Latitude: 48.8566, Longitude: 2.3522}}}
	if providerCacheKey("chat", request) != "" {
		t.Fatal("exact cache allowed a Google Maps request")
	}
	if _, _, ok := semanticRequest(request, Endpoint{Name: "test"}); ok {
		t.Fatal("semantic cache allowed a Google Maps request")
	}
	if got := strings.Join(requiredChatCapabilities(request.Request, false), ","); got != "chat,google_maps" {
		t.Fatalf("Google Maps routing requirements=%s", got)
	}
}

func TestVertexAudioTimestampRequiresAudioCapabilities(t *testing.T) {
	enabled := true
	request := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{
		Model: "test",
		Messages: []openai.Message{{Role: "user", Content: []any{
			map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "UklGRgAAAABXQVZF", "format": "wav"}},
		}}},
		GeminiAudioTimestamp: &enabled,
	}}
	if providerCacheKey("chat", request) != "" {
		t.Fatal("exact cache allowed timestamp-aware audio")
	}
	if _, _, ok := semanticRequest(request, Endpoint{Name: "test"}); ok {
		t.Fatal("semantic cache allowed timestamp-aware audio")
	}
	if got := strings.Join(requiredChatCapabilities(request.Request, false), ","); got != "chat,audio_input,gemini_audio_timestamp" {
		t.Fatalf("audio timestamp routing requirements=%s", got)
	}
}

func TestGeminiMediaResolutionRequiresMediaCapabilities(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{
		Model: "test",
		Messages: []openai.Message{{Role: "user", Content: []any{
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo="}},
		}}},
		GeminiMediaResolution: "MEDIA_RESOLUTION_HIGH",
	}}
	changed := request
	changed.Request.GeminiMediaResolution = "MEDIA_RESOLUTION_LOW"
	if providerCacheKey("chat", request) == providerCacheKey("chat", changed) {
		t.Fatal("exact cache ignored media resolution")
	}
	if _, _, ok := semanticRequest(request, Endpoint{Name: "test"}); ok {
		t.Fatal("semantic cache allowed media-resolution request")
	}
	if got := strings.Join(requiredChatCapabilities(request.Request, false), ","); got != "chat,vision,gemini_media_resolution" {
		t.Fatalf("media resolution routing requirements=%s", got)
	}
	request.Request.GeminiMediaResolution = ""
	part := request.Request.Messages[0].Content.([]any)[0].(map[string]any)
	part["gemini_media_resolution"] = "MEDIA_RESOLUTION_ULTRA_HIGH"
	if got := strings.Join(requiredChatCapabilities(request.Request, false), ","); got != "chat,vision,gemini_media_resolution" {
		t.Fatalf("per-part media resolution routing requirements=%s", got)
	}
}

func TestGeminiMediaProcessingRequiresVideoCapability(t *testing.T) {
	part := map[string]any{
		"type": "input_video", "input_video": map[string]any{"data": "AAAADGZ0eXBtcDQy", "format": "mp4"},
		"gemini_media_processing": "STATIC",
	}
	request := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: []any{part}}}}}
	if providerCacheKey("chat", request) != "" {
		t.Fatal("exact cache allowed media-processing video request")
	}
	if _, _, ok := semanticRequest(request, Endpoint{Name: "test"}); ok {
		t.Fatal("semantic cache allowed media-processing request")
	}
	if got := strings.Join(requiredChatCapabilities(request.Request, false), ","); got != "chat,video_input,gemini_media_processing" {
		t.Fatalf("media processing routing requirements=%s", got)
	}
}

func TestChatWebFetchDisablesResponseCaches(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "read https://example.com"}}, ChatGenerationOptions: openai.ChatGenerationOptions{WebFetchOptions: &openai.ChatWebFetchOptions{AllowedDomains: []string{"example.com"}, MaxContentTokens: 1000}}}}
	if providerCacheKey("chat", request) != "" {
		t.Fatal("exact cache allowed a web fetch request")
	}
	if _, _, ok := semanticRequest(request, Endpoint{Name: "test"}); ok {
		t.Fatal("semantic cache allowed a web fetch request")
	}
	if got := strings.Join(requiredChatCapabilities(request.Request, true), ","); got != "chat,stream,web_fetch" {
		t.Fatalf("web fetch routing requirements=%s", got)
	}
}

func TestPromptCacheKeyScopesCaches(t *testing.T) {
	base := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "hello"}}}}
	changed := base
	changed.Request.PromptCacheKey = "tenant-thread"
	if providerCacheKey("chat", base) == providerCacheKey("chat", changed) {
		t.Fatal("exact cache ignored prompt_cache_key")
	}
	baseScope, _, baseOK := semanticRequest(base, Endpoint{Name: "test"})
	changedScope, _, changedOK := semanticRequest(changed, Endpoint{Name: "test"})
	if !baseOK || !changedOK || baseScope == changedScope {
		t.Fatal("semantic cache ignored prompt_cache_key")
	}
}

func TestPromptCacheOptionsScopeCaches(t *testing.T) {
	base := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "hello"}}}}
	changed := base
	changed.Request.PromptCacheOptions = &openai.PromptCacheOptions{Mode: "explicit", TTL: "30m"}
	if providerCacheKey("chat", base) == providerCacheKey("chat", changed) {
		t.Fatal("exact cache ignored prompt_cache_options")
	}
	baseScope, _, baseOK := semanticRequest(base, Endpoint{Name: "test"})
	changedScope, _, changedOK := semanticRequest(changed, Endpoint{Name: "test"})
	if !baseOK || !changedOK || baseScope == changedScope {
		t.Fatal("semantic cache ignored prompt_cache_options")
	}
}

func TestPromptCacheRetentionScopesCaches(t *testing.T) {
	base := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "hello"}}}}
	changed := base
	changed.Request.PromptCacheRetention = "24h"
	if providerCacheKey("chat", base) == providerCacheKey("chat", changed) {
		t.Fatal("exact cache ignored prompt_cache_retention")
	}
	baseScope, _, baseOK := semanticRequest(base, Endpoint{Name: "test"})
	changedScope, _, changedOK := semanticRequest(changed, Endpoint{Name: "test"})
	if !baseOK || !changedOK || baseScope == changedScope {
		t.Fatal("semantic cache ignored prompt_cache_retention")
	}
}

func TestPromptCacheBreakpointsDisableSemanticCacheAndScopeExactCache(t *testing.T) {
	base := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: []any{map[string]any{"type": "text", "text": "hello"}}}}}}
	changed := base
	changed.Request.Messages = []openai.Message{{Role: "user", Content: []any{map[string]any{"type": "text", "text": "hello", "prompt_cache_breakpoint": map[string]any{"mode": "explicit"}}}}}
	if providerCacheKey("chat", base) == providerCacheKey("chat", changed) {
		t.Fatal("exact cache ignored prompt_cache_breakpoint")
	}
	if _, _, ok := semanticRequest(changed, Endpoint{Name: "test"}); ok {
		t.Fatal("semantic cache bypassed explicit prompt cache breakpoint")
	}
}

func TestPredictionScopesCaches(t *testing.T) {
	base := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "hello"}}}}
	changed := base
	changed.Request.Prediction = &openai.ChatPrediction{Type: "content", Content: "expected"}
	if providerCacheKey("chat", base) == providerCacheKey("chat", changed) {
		t.Fatal("exact cache ignored prediction")
	}
	baseScope, _, baseOK := semanticRequest(base, Endpoint{Name: "test"})
	changedScope, _, changedOK := semanticRequest(changed, Endpoint{Name: "test"})
	if !baseOK || !changedOK || baseScope == changedScope {
		t.Fatal("semantic cache ignored prediction")
	}
}

func TestVerbosityScopesCaches(t *testing.T) {
	base := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "hello"}}}}
	changed := base
	changed.Request.Verbosity = "low"
	if providerCacheKey("chat", base) == providerCacheKey("chat", changed) {
		t.Fatal("exact cache ignored verbosity")
	}
	baseScope, _, baseOK := semanticRequest(base, Endpoint{Name: "test"})
	changedScope, _, changedOK := semanticRequest(changed, Endpoint{Name: "test"})
	if !baseOK || !changedOK || baseScope == changedScope {
		t.Fatal("semantic cache ignored verbosity")
	}
}

func TestChatModalitiesScopeCaches(t *testing.T) {
	base := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "hello"}}}}
	changed := base
	changed.Request.Modalities = []string{"text"}
	if providerCacheKey("chat", base) == providerCacheKey("chat", changed) {
		t.Fatal("exact cache ignored modalities")
	}
	baseScope, _, baseOK := semanticRequest(base, Endpoint{Name: "test"})
	changedScope, _, changedOK := semanticRequest(changed, Endpoint{Name: "test"})
	if !baseOK || !changedOK || baseScope == changedScope {
		t.Fatal("semantic cache ignored modalities")
	}
}

func TestChatMetadataAndStoreScopeCaches(t *testing.T) {
	base := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "hello"}}}}
	store := false
	for name, change := range map[string]func(*openai.ChatCompletionRequest){
		"metadata": func(request *openai.ChatCompletionRequest) { request.Metadata = map[string]string{"trace": "one"} },
		"store":    func(request *openai.ChatCompletionRequest) { request.Store = &store },
	} {
		t.Run(name, func(t *testing.T) {
			changed := base
			change(&changed.Request)
			if providerCacheKey("chat", base) == providerCacheKey("chat", changed) {
				t.Fatalf("exact cache ignored %s", name)
			}
			baseScope, _, baseOK := semanticRequest(base, Endpoint{Name: "test"})
			changedScope, _, changedOK := semanticRequest(changed, Endpoint{Name: "test"})
			if !baseOK || !changedOK || baseScope == changedScope {
				t.Fatalf("semantic cache ignored %s", name)
			}
		})
	}
}

func TestStoredChatBypassesResponseCaches(t *testing.T) {
	store := true
	request := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{Store: &store}}}
	if key := providerCacheKey("chat", request); key != "" {
		t.Fatalf("stored chat exact cache key=%q", key)
	}
	if scope, text, ok := semanticRequest(request, Endpoint{Name: "test"}); ok || scope != "" || text != "" {
		t.Fatalf("stored chat semantic cache request=(%q, %q, %t)", scope, text, ok)
	}
}
