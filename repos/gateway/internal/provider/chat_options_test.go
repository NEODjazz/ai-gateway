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
			if err := json.Unmarshal([]byte(`{"model":"test","metadata":{"trace":"one"},"store":false,"reasoning_effort":"high","n":2,"safety_identifier":"hashed-user","prompt_cache_key":"tenant-thread","prompt_cache_options":{"mode":"explicit","ttl":"30m"},"verbosity":"low","logprobs":true,"top_logprobs":0,"frequency_penalty":0,"presence_penalty":-1,"logit_bias":{"10":-100}}`), &request); err != nil {
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
			for name, want := range map[string]string{"metadata": `{"trace":"one"}`, "store": "false", "reasoning_effort": `"high"`, "n": "2", "safety_identifier": `"hashed-user"`, "prompt_cache_key": `"tenant-thread"`, "prompt_cache_options": `{"mode":"explicit","ttl":"30m"}`, "verbosity": `"low"`, "logprobs": "true", "top_logprobs": "0", "frequency_penalty": "0", "presence_penalty": "-1", "logit_bias": `{"10":-100}`} {
				if string(received[name]) != want {
					t.Fatalf("%s=%s, want %s", name, received[name], want)
				}
			}
			if streaming {
				if string(received["stream_options"]) != `{"include_usage":true}` {
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
		"negative timestamp": `{"created":-1,"choices":[]}`,
		"oversized metadata": `{"metadata":{"trace":"` + strings.Repeat("x", 513) + `"},"choices":[]}`,
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
	for _, body := range []string{`{"metadata":{"trace":"one"}}`, `{"store":false}`, `{"reasoning_effort":"high"}`, `{"n":2}`, `{"safety_identifier":"hashed-user"}`, `{"prompt_cache_key":"tenant-thread"}`, `{"prompt_cache_options":{"mode":"explicit"}}`, `{"service_tier":"priority"}`, `{"verbosity":"low"}`, `{"logprobs":false}`, `{"top_logprobs":0}`, `{"frequency_penalty":0}`, `{"presence_penalty":0}`, `{"logit_bias":{"1":0}}`} {
		var request openai.ChatCompletionRequest
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			t.Fatal(err)
		}
		for _, client := range []Client{NewAnthropic("http://unused.invalid", "", false), NewOllama("http://unused.invalid", false), Demo{}} {
			if err := validateChatAdapter(client, request); err == nil {
				t.Fatalf("%T silently accepted %s", client, body)
			}
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
