package gateway

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

func generateCall(handler http.Handler, path, body, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest("POST", path, strings.NewReader(body))
	request.Header.Set("x-goog-api-key", key)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
func TestGenerateContentNativeJSON(t *testing.T) {
	upstream := &fallbackChatProvider{response: openai.ChatCompletionResponse{ID: "id", Model: "m", Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "call", Type: "function", Function: openai.FunctionCall{Name: "weather", Arguments: `{"city":"Paris"}`}, ExtraContent: &openai.ToolCallExtraContent{Google: &openai.GoogleToolCallContent{ThoughtSignature: "opaque"}}}}}, FinishReason: "tool_calls"}}, Usage: openai.Usage{PromptTokens: 10, CompletionTokens: 7, TotalTokens: 17, CompletionTokensDetails: &openai.CompletionTokenDetails{ReasoningTokens: 4}}}}
	handler := Routes(NewHandler(modules.NewPipeline(nil), upstream))
	response := generateCall(handler, "/v1beta/models/m:generateContent", `{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":20}}`, "")
	if response.Code != 200 || upstream.calls != 1 || upstream.request.Request.MaxCompletionTokens == nil || *upstream.request.Request.MaxCompletionTokens != 20 {
		t.Fatalf("request: %d %s", response.Code, response.Body.String())
	}
	for _, want := range []string{`"functionCall"`, `"thoughtSignature":"opaque"`, `"finishReason":"STOP"`, `"thoughtsTokenCount":4`, `"candidatesTokenCount":3`, `"totalTokenCount":17`} {
		if !strings.Contains(response.Body.String(), want) {
			t.Fatalf("missing %s: %s", want, response.Body.String())
		}
	}
}
func TestGenerateContentAuthQuotasAndUnsupportedParameters(t *testing.T) {
	for _, tc := range []struct {
		path, body, key string
		policy          accessPolicyModule
		code            int
	}{
		{path: "/v1beta/models/m:generateContent", body: `{"contents":[{"parts":[{"text":"hi"}]}]}`, code: 401},
		{path: "/v1beta/models/m:generateContent", body: `{"contents":[{"parts":[{"text":"hi"}]}]}`, key: "gateway-test-key", policy: accessPolicyModule{models: []string{"other"}}, code: 403},
		{path: "/v1beta/models/m:generateContent", body: `{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":10}}`, key: "gateway-test-key", policy: accessPolicyModule{models: []string{"*"}, tpm: 1}, code: 429},
		{path: "/v1beta/models/m:streamGenerateContent", body: `{}`, code: 400},
		{path: "/v1beta/models/m:generateContent?key=not-accepted", body: `{}`, code: 400},
		{path: "/v1beta/models/m:generateContent", body: `{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"candidateCount":2}}`, code: 400},
		{path: "/v1beta/models/m:generateContent", body: `{"contents":[{"parts":[{"text":"hi"}]}],"unknown":true}`, code: 400},
		{path: "/v1beta/models/m:missing", body: `{}`, code: 404},
	} {
		upstream := &fallbackChatProvider{}
		handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{tc.policy}}), upstream))
		response := generateCall(handler, tc.path, tc.body, tc.key)
		if response.Code != tc.code || upstream.calls != 0 || !strings.Contains(response.Body.String(), `"status":`) {
			t.Fatalf("policy: %d %s calls=%d", response.Code, response.Body.String(), upstream.calls)
		}
	}
}
func TestGenerateContentSSEConvertsToolsAndErrors(t *testing.T) {
	for _, fail := range []bool{false, true} {
		upstream := &nativeMessagesStreamProvider{fail: fail}
		response := generateCall(Routes(NewHandler(modules.NewPipeline(nil), upstream)), "/v1beta/models/model:streamGenerateContent?alt=sse", `{"contents":[{"parts":[{"text":"hi"}]}]}`, "")
		body := response.Body.String()
		if response.Code != 200 || response.Header().Get("Content-Type") != "text/event-stream" || strings.Contains(body, "[DONE]") || strings.Contains(body, "sensitive upstream error") {
			t.Fatalf("native stream: %d %s", response.Code, body)
		}
		if fail {
			if !strings.Contains(body, `"error":`) || strings.Contains(body, `"finishReason"`) {
				t.Fatal(body)
			}
			continue
		}
		if !strings.Contains(body, `"args":{"city":"Paris"}`) || !strings.Contains(body, `"finishReason":"STOP"`) || !strings.Contains(body, `"promptTokenCount":12`) {
			t.Fatal(body)
		}
	}
}
func TestGenerateContentGeminiRoundTripUsageAndBilling(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-test:streamGenerateContent" || r.Header.Get("x-goog-api-key") != "provider-key" {
			t.Error("native path/key lost")
		}
		_, _ = w.Write([]byte("data: {\"responseId\":\"g-test\",\"modelVersion\":\"gemini-resolved\",\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"hi\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":2,\"thoughtsTokenCount\":3,\"totalTokenCount\":15}}\n\n"))
	}))
	defer upstream.Close()
	billing := &messagesUsageRecorder{}
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "native", Type: "gemini", BaseURL: upstream.URL, APIKey: "provider-key", Stream: true, Models: []string{"m"}, ModelAliases: map[string]string{"m": "gemini-test"}}}, Modules: modules.NewPipeline([]modules.Module{billing})})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"m"}}}}), router))
	response := generateCall(handler, "/v1beta/models/m:streamGenerateContent?alt=sse", `{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":10}}`, "gateway-test-key")
	if !strings.Contains(response.Body.String(), `"modelVersion":"gemini-resolved"`) || response.Code != 200 || billing.calls != 1 || billing.usage.TotalTokens != 15 || billing.usage.CompletionTokens != 5 || !strings.Contains(response.Body.String(), `"candidatesTokenCount":2`) || !strings.Contains(response.Body.String(), `"thoughtsTokenCount":3`) {
		t.Fatalf("native usage: %d %+v %s", response.Code, billing.usage, response.Body.String())
	}
}
func TestGenerateContentStreamingBoundsAndWriteFailure(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := &generateWriter{destination: recorder, headers: make(http.Header)}
	if err := writer.chunk(`{"id":"m","choices":[{"index":0,"delta":{"content":"hi"}}]}`); err != nil {
		t.Fatal(err)
	}
	if err := writer.chunk("[DONE]"); err == nil {
		t.Fatal("truncated stream accepted")
	}
	writer.finish()
	if !strings.Contains(recorder.Body.String(), `"error"`) {
		t.Fatal("stream failure not emitted")
	}
	bounded := &generateWriter{destination: httptest.NewRecorder(), headers: make(http.Header)}
	if _, err := bounded.Write(make([]byte, (32<<20)+1)); err == nil {
		t.Fatal("oversized frame accepted")
	}
	failure := errors.New("disconnected")
	failed := &generateWriter{destination: messagesFailWriter{httptest.NewRecorder(), failure}, headers: make(http.Header)}
	if err := failed.chunk(`{"id":"m","choices":[{"index":0,"delta":{"content":"hi"}}]}`); !errors.Is(err, failure) {
		t.Fatal("write error lost")
	}
}
func TestGenerateContentMetricPathsAreBounded(t *testing.T) {
	for _, action := range []string{"generateContent", "streamGenerateContent"} {
		if got := metricPath("/v1beta/models/private-model:" + action); got != "/v1beta/models/{model}:"+action {
			t.Fatalf("metric path %s", got)
		}
	}
}

func TestGenerateContentEnforcesToolACL(t *testing.T) {
	upstream := &fallbackChatProvider{}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"*"}, tools: []string{"safe"}}}}), upstream))
	response := generateCall(handler, "/v1beta/models/m:generateContent", `{"contents":[{"parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"denied"}]}]}`, "gateway-test-key")
	if response.Code != 403 || upstream.calls != 0 {
		t.Fatalf("tool ACL bypassed: %d %s", response.Code, response.Body.String())
	}
}
func TestGenerateContentFallbackSSE(t *testing.T) {
	upstream := &fallbackChatProvider{response: openai.ChatCompletionResponse{ID: "fallback", Model: "m", Choices: []openai.Choice{{Message: openai.Message{Role: "assistant", Content: "hello"}, FinishReason: "stop"}}, Usage: openai.Usage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3}}}
	response := generateCall(Routes(NewHandler(modules.NewPipeline(nil), upstream)), "/v1beta/models/m:streamGenerateContent?alt=sse", `{"contents":[{"parts":[{"text":"hi"}]}]}`, "")
	for _, want := range []string{`"text":"hello"`, `"finishReason":"STOP"`, `"totalTokenCount":3`} {
		if !strings.Contains(response.Body.String(), want) {
			t.Fatalf("missing %s: %s", want, response.Body.String())
		}
	}
}
func TestGenerateContentRejectsMalformedToolStreams(t *testing.T) {
	for _, payload := range []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":128}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":-1}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"type":"custom"}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"new"}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"extra_content":{"google":{"thought_signature":"changed"}}}]}}]}`,
	} {
		writer := &generateWriter{destination: httptest.NewRecorder(), headers: make(http.Header)}
		first := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"original","function":{"name":"f"},"extra_content":{"google":{"thought_signature":"original"}}}]}}]}`
		if err := writer.chunk(first); err != nil {
			t.Fatal(err)
		}
		if err := writer.chunk(payload); err == nil {
			t.Fatalf("accepted malformed tool: %s", payload)
		}
	}
	for _, args := range []string{"null", "[]", "{"} {
		_, err := generateParts(openai.Message{ToolCalls: []openai.ToolCall{{Type: "function", Function: openai.FunctionCall{Name: "f", Arguments: args}}}})
		if err == nil {
			t.Fatalf("accepted non-object args: %s", args)
		}
	}
	writer := &generateWriter{destination: httptest.NewRecorder(), headers: make(http.Header), toolBytes: 32 << 20}
	if err := writer.chunk(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"x"}}]}}]}`); err == nil {
		t.Fatal("tool budget exceeded")
	}
}
