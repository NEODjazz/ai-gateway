package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestInferenceEndpointsRejectUnsupportedParameters(t *testing.T) {
	for field, value := range map[string]string{"unsupported_future_option": `"high"`, "background": "true"} {
		for _, endpoint := range []string{"chat", "responses", "embeddings", "rerank"} {
			t.Run(endpoint+"/"+field, func(t *testing.T) {
				access := &countingAccessModule{}
				handler := NewHandler(modules.NewPipeline([]modules.Module{access}), &chatProvider{})
				invoke := map[string]http.HandlerFunc{"chat": handler.ChatCompletions, "responses": handler.Responses, "embeddings": handler.Embeddings, "rerank": handler.Rerank}[endpoint]
				request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"model":"test","`+field+`":`+value+`}`))
				response := httptest.NewRecorder()
				invoke(response, request)
				var result struct {
					Error struct {
						Code    string `json:"code"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if response.Code != http.StatusBadRequest || result.Error.Code != "invalid_request" || !strings.Contains(result.Error.Message, `unknown field "`+field+`"`) {
					t.Fatalf("unsupported parameter did not produce a clear error: %d %s", response.Code, response.Body.String())
				}
				if access.calls != 0 {
					t.Fatal("unsupported request reached pipeline")
				}
			})
		}
	}
}

func TestNonGenerationEndpointsStillRejectServiceTierAsUnknown(t *testing.T) {
	for _, endpoint := range []string{"embeddings", "rerank"} {
		t.Run(endpoint, func(t *testing.T) {
			handler := NewHandler(modules.NewPipeline(nil), &chatProvider{})
			invoke := map[string]http.HandlerFunc{"embeddings": handler.Embeddings, "rerank": handler.Rerank}[endpoint]
			response := httptest.NewRecorder()
			invoke(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"model":"test","service_tier":"priority"}`)))
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "unknown field") || !strings.Contains(response.Body.String(), "service_tier") {
				t.Fatalf("service tier unexpectedly entered %s contract: %d %s", endpoint, response.Code, response.Body.String())
			}
		})
	}
}

func TestInferenceDecoderPreservesToolSchemas(t *testing.T) {
	body := `{"model":"test","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"test","parameters":{"type":"object","properties":{"reasoning_effort":{"type":"string"},"logprobs":{"type":"boolean"}},"additionalProperties":false}}}],"response_format":{"type":"json_schema","json_schema":{"name":"result","schema":{"type":"object","properties":{"service_tier":{"type":"string"}}}}}}`
	var request openai.ChatCompletionRequest
	response := httptest.NewRecorder()
	if !decodeInferenceRequest(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), &request) {
		t.Fatalf("valid schema rejected: %s", response.Body.String())
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"reasoning_effort", "logprobs", "service_tier", "additionalProperties"} {
		if !strings.Contains(string(encoded), name) {
			t.Fatalf("schema property %s was lost", name)
		}
	}
}

func TestInferenceDecoderRejectsUnknownMessageField(t *testing.T) {
	var request openai.ChatCompletionRequest
	response := httptest.NewRecorder()
	if decodeInferenceRequest(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"model":"test","messages":[{"role":"user","content":"hello","unsupported":true}]}`)), &request) {
		t.Fatal("unknown message field accepted")
	}
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestSyntheticChatStreamPreservesLogprobs(t *testing.T) {
	response := httptest.NewRecorder()
	writeChatCompletionStream(response, openai.ChatCompletionResponse{
		ID: "chat-test", Created: 123, Model: "test", Metadata: map[string]string{"trace": "one"}, ServiceTier: "priority", SystemFingerprint: "fp-test",
		Choices: []openai.Choice{{Message: openai.Message{Role: "assistant", Content: "hello"}, Logprobs: &openai.ChoiceLogprobs{Content: []openai.TokenLogprob{{Token: "hello", Logprob: -0.5}}}}},
	})
	if !strings.Contains(response.Body.String(), `"logprobs":{"content":[{"token":"hello","logprob":-0.5`) {
		t.Fatalf("synthetic SSE dropped logprobs: %s", response.Body.String())
	}
	for _, field := range []string{`"metadata":{"trace":"one"}`, `"service_tier":"priority"`, `"system_fingerprint":"fp-test"`} {
		if !strings.Contains(response.Body.String(), field) {
			t.Fatalf("synthetic SSE dropped response envelope %s: %s", field, response.Body.String())
		}
	}
	if count := strings.Count(response.Body.String(), `"created":123`); count != 3 {
		t.Fatalf("synthetic SSE did not reuse the upstream timestamp in every event: count=%d body=%s", count, response.Body.String())
	}
}

func TestChatRejectsInvalidGenerationOptionsBeforePipeline(t *testing.T) {
	for _, test := range []struct{ body, message string }{
		{`{"model":"test","messages":[],"top_logprobs":2}`, "requires logprobs=true"},
		{`{"model":"test","messages":[],"n":0}`, "n must be between 1 and 128"},
		{`{"model":"test","messages":[],"safety_identifier":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, "at most 64 characters"},
		{`{"model":"test","messages":[],"service_tier":"unknown"}`, "unsupported service_tier value"},
		{`{"model":"test","messages":[],"verbosity":"unknown"}`, "verbosity must be low, medium, or high"},
	} {
		access := &countingAccessModule{}
		handler := NewHandler(modules.NewPipeline([]modules.Module{access}), &chatProvider{})
		response := httptest.NewRecorder()
		handler.ChatCompletions(response, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(test.body)))
		if response.Code != 400 || !strings.Contains(response.Body.String(), test.message) || access.calls != 0 {
			t.Fatalf("invalid generation options: status=%d body=%s pipeline=%d", response.Code, response.Body.String(), access.calls)
		}
	}
}

func TestChatMultiChoiceUsesAggregateTPMReserve(t *testing.T) {
	store := &embeddingTokenRateStore{}
	maxTokens, choices := 20, 3
	request := openai.ChatCompletionRequest{
		ChatGenerationOptions: openai.ChatGenerationOptions{N: &choices},
		Model:                 "test", Messages: []openai.Message{{Role: "user", Content: "hello"}}, MaxCompletionTokens: &maxTokens,
	}
	handler := NewHandlerWithRateLimitStore(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}, tpm: 1000}}), &chatProvider{}, store)
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ChatCompletions(response, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body))))
	if response.Code != http.StatusOK || store.tokens != openai.ChatReserveTokens(request) {
		t.Fatalf("multi-choice TPM reserve mismatch: status=%d tokens=%d want=%d body=%s", response.Code, store.tokens, openai.ChatReserveTokens(request), response.Body.String())
	}
}
