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
	for field, value := range map[string]string{"unsupported_future_option": `"high"`, "background": "true", "service_tier": `"priority"`} {
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
	writeChatCompletionStream(response, openai.ChatCompletionResponse{Choices: []openai.Choice{{Message: openai.Message{Role: "assistant", Content: "hello"}, Logprobs: &openai.ChoiceLogprobs{Content: []openai.TokenLogprob{{Token: "hello", Logprob: -0.5}}}}}})
	if !strings.Contains(response.Body.String(), `"logprobs":{"content":[{"token":"hello","logprob":-0.5`) {
		t.Fatalf("synthetic SSE dropped logprobs: %s", response.Body.String())
	}
}

func TestChatRejectsInvalidGenerationOptionsBeforePipeline(t *testing.T) {
	access := &countingAccessModule{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{access}), &chatProvider{})
	response := httptest.NewRecorder()
	handler.ChatCompletions(response, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test","messages":[],"top_logprobs":2}`)))
	if response.Code != 400 || !strings.Contains(response.Body.String(), "requires logprobs=true") || access.calls != 0 {
		t.Fatalf("invalid generation options: status=%d body=%s pipeline=%d", response.Code, response.Body.String(), access.calls)
	}
}
