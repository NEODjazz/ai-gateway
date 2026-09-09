package gateway

import (
	"context"
	"encoding/json"
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

func nativeMessageCall(handler http.Handler, body string, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	request.Header.Set("anthropic-version", "2023-06-01")
	request.Header.Set("x-api-key", key)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
func TestMessagesConvertsToolsAndResponse(t *testing.T) {
	upstream := &fallbackChatProvider{response: openai.ChatCompletionResponse{ID: "msg-test", Model: "model", Choices: []openai.Choice{{Message: openai.Message{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "call-next", Type: "function", Function: openai.FunctionCall{Name: "weather", Arguments: `{"city":"Rome"}`}}}}, FinishReason: "tool_calls"}}, Usage: openai.Usage{PromptTokens: 20, CompletionTokens: 5, TotalTokens: 25, PromptTokensDetails: &openai.PromptTokenDetails{CachedTokens: 4, CacheWriteTokens: 3}}}}
	handler := Routes(NewHandler(modules.NewPipeline(nil), upstream))
	response := nativeMessageCall(handler, `{"model":"model","max_tokens":80,"system":[{"type":"text","text":"Be helpful"}],"tools":[{"name":"weather","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"weather","disable_parallel_tool_use":true},"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call-old","name":"weather","input":{"city":"Paris"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-old","content":[{"type":"text","text":"Sunny"}]}]}]}`, "")
	if response.Code != 200 {
		t.Fatalf("response: %d %s", response.Code, response.Body.String())
	}
	request := upstream.request.Request
	if upstream.calls != 1 || *request.MaxTokens != 80 || len(request.Messages) != 3 || request.Messages[0].Content != "Be helpful" || request.Messages[2].Role != "tool" || request.Messages[2].ToolCallID != "call-old" || request.ParallelToolCalls == nil || *request.ParallelToolCalls {
		t.Fatalf("conversion: %+v", request)
	}
	var body struct {
		Type    string `json:"type"`
		Stop    string `json:"stop_reason"`
		Content []struct {
			Type  string         `json:"type"`
			ID    string         `json:"id"`
			Input map[string]any `json:"input"`
		} `json:"content"`
		Usage map[string]int `json:"usage"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Type != "message" || body.Stop != "tool_use" || len(body.Content) != 1 || body.Content[0].Input["city"] != "Rome" || body.Usage["input_tokens"] != 13 || body.Usage["output_tokens"] != 5 || body.Usage["cache_creation_input_tokens"] != 3 {
		t.Fatalf("native response: %s", response.Body.String())
	}
}

func TestMessagesConvertsMetadataOutputConfigAndUsageDetails(t *testing.T) {
	upstream := &fallbackChatProvider{response: openai.ChatCompletionResponse{
		ID: "message", Model: "model", Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", Content: `{"ok":true}`}, FinishReason: "stop"}},
		Usage: openai.Usage{PromptTokens: 8, CompletionTokens: 5, TotalTokens: 13, CompletionTokensDetails: &openai.CompletionTokenDetails{ReasoningTokens: 3}},
	}}
	handler := Routes(NewHandler(modules.NewPipeline(nil), upstream))
	response := nativeMessageCall(handler, `{"model":"model","max_tokens":20,"metadata":{"user_id":"customer-42"},"output_config":{"effort":"high","format":{"type":"json_schema","schema":{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}}}}},"messages":[{"role":"user","content":"answer"}]}`, "")
	if response.Code != http.StatusOK || upstream.calls != 1 {
		t.Fatalf("response=%d body=%s calls=%d", response.Code, response.Body.String(), upstream.calls)
	}
	request := upstream.request.Request
	if request.Metadata["user_id"] != "customer-42" || request.ReasoningEffort != "high" || request.ResponseFormat == nil || request.ResponseFormat.Type != "json_schema" || request.ResponseFormat.JSONSchema == nil {
		t.Fatalf("native controls lost: %+v", request)
	}
	var body struct {
		Usage struct {
			OutputTokensDetails struct {
				ThinkingTokens int `json:"thinking_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Usage.OutputTokensDetails.ThinkingTokens != 3 {
		t.Fatalf("usage details lost: err=%v body=%s", err, response.Body.String())
	}
}

func nativeServerToolResponse() openai.ChatCompletionResponse {
	return openai.ChatCompletionResponse{
		ID: "msg-native", Model: "model",
		Choices: []openai.Choice{{Index: 0, FinishReason: "stop", Message: openai.Message{Role: "assistant", NativeContent: []json.RawMessage{
			json.RawMessage(`{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"weather"}}`),
			json.RawMessage(`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","url":"https://example.com","title":"Weather","encrypted_content":"opaque"}]}`),
			json.RawMessage(`{"type":"text","text":"Sunny","citations":[{"type":"web_search_result_location","url":"https://example.com","title":"Weather","cited_text":"Sunny"}]}`),
		}}}},
		Usage: openai.Usage{PromptTokens: 4, CompletionTokens: 3, TotalTokens: 7, SearchRequests: 1},
	}
}

func TestMessagesPreservesNativeServerToolBlocks(t *testing.T) {
	upstream := &fallbackChatProvider{response: nativeServerToolResponse()}
	handler := Routes(NewHandler(modules.NewPipeline(nil), upstream))
	response := nativeMessageCall(handler, `{"model":"model","max_tokens":20,"messages":[{"role":"user","content":"weather"}]}`, "")
	if response.Code != http.StatusOK || upstream.calls != 1 {
		t.Fatalf("response=%d body=%s calls=%d", response.Code, response.Body.String(), upstream.calls)
	}
	var body struct {
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Content) != 3 || body.Content[0]["type"] != "server_tool_use" || body.Content[1]["type"] != "web_search_tool_result" || body.Content[2]["type"] != "text" || !strings.Contains(response.Body.String(), `"encrypted_content":"opaque"`) {
		t.Fatalf("native blocks changed: %s", response.Body.String())
	}
}

func TestMessagesSynthesizesNativeServerToolSSE(t *testing.T) {
	upstream := &fallbackChatProvider{response: nativeServerToolResponse()}
	handler := Routes(NewHandler(modules.NewPipeline(nil), upstream))
	response := nativeMessageCall(handler, `{"model":"model","max_tokens":20,"stream":true,"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":2}],"messages":[{"role":"user","content":"weather"}]}`, "")
	body := response.Body.String()
	if response.Code != http.StatusOK || upstream.calls != 1 || upstream.request.Request.Stream || upstream.request.Request.WebSearchOptions == nil || upstream.request.Request.WebSearchOptions.MaxUses == nil || *upstream.request.Request.WebSearchOptions.MaxUses != 2 {
		t.Fatalf("response=%d body=%s calls=%d stream=%v", response.Code, body, upstream.calls, upstream.request.Request.Stream)
	}
	for _, expected := range []string{
		"event: message_start", `"type":"server_tool_use"`, `"type":"web_search_tool_result"`, `"encrypted_content":"opaque"`,
		`"type":"text_delta"`, `"text":"Sunny"`, `"type":"citations_delta"`, `"stop_reason":"end_turn"`, "event: message_stop",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %q in %s", expected, body)
		}
	}
	if strings.Index(body, `"type":"server_tool_use"`) > strings.Index(body, `"type":"web_search_tool_result"`) || strings.Index(body, `"type":"web_search_tool_result"`) > strings.Index(body, `"type":"text_delta"`) {
		t.Fatalf("native stream order changed: %s", body)
	}
}

func TestMessagesConvertsBoundedNativeServerTools(t *testing.T) {
	upstream := &fallbackChatProvider{response: nativeServerToolResponse()}
	handler := Routes(NewHandler(modules.NewPipeline(nil), upstream))
	response := nativeMessageCall(handler, `{"model":"model","max_tokens":20,"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":2,"user_location":{"type":"approximate","approximate":{"country":"FR"}}},{"type":"web_fetch_20250910","name":"web_fetch","allowed_domains":["docs.example.com"],"max_uses":1,"max_content_tokens":500,"citations":{"enabled":true}}],"messages":[{"role":"user","content":"research"}]}`, "")
	request := upstream.request.Request
	if response.Code != http.StatusOK || upstream.calls != 1 || request.WebSearchOptions == nil || request.WebSearchOptions.MaxUses == nil || *request.WebSearchOptions.MaxUses != 2 || request.WebSearchOptions.UserLocation == nil || request.WebSearchOptions.UserLocation.Approximate.Country != "FR" {
		t.Fatalf("search tool conversion failed: status=%d body=%s request=%+v", response.Code, response.Body.String(), request)
	}
	if request.WebFetchOptions == nil || request.WebFetchOptions.MaxUses == nil || *request.WebFetchOptions.MaxUses != 1 || request.WebFetchOptions.MaxContentTokens != 500 || len(request.WebFetchOptions.AllowedDomains) != 1 || request.WebFetchOptions.AllowedDomains[0] != "docs.example.com" {
		t.Fatalf("fetch tool conversion failed: %+v", request.WebFetchOptions)
	}
}

func TestMessagesRejectsInvalidNativeProviderBlocks(t *testing.T) {
	for _, stream := range []bool{false, true} {
		upstream := &fallbackChatProvider{response: nativeServerToolResponse()}
		upstream.response.Choices[0].Message.NativeContent = []json.RawMessage{json.RawMessage(`{"type":"server_tool_use","id":"missing-input","name":"web_search"}`)}
		handler := Routes(NewHandler(modules.NewPipeline(nil), upstream))
		body := `{"model":"model","max_tokens":20,"messages":[{"role":"user","content":"weather"}]}`
		if stream {
			body = `{"model":"model","max_tokens":20,"stream":true,"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"weather"}]}`
		}
		response := nativeMessageCall(handler, body, "")
		if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "missing-input") {
			t.Fatalf("stream=%v response=%d body=%s", stream, response.Code, response.Body.String())
		}
	}
}

type messagesAuth struct{ accessPolicyModule }

func (m messagesAuth) Handle(ctx context.Context, req *modules.RequestContext) error {
	if req.APIKey != "gateway-test-key" {
		return modules.ErrUnauthorized
	}
	return m.accessPolicyModule.Handle(ctx, req)
}
func TestMessagesPreservesAccessControls(t *testing.T) {
	for _, test := range []struct {
		name, key, body string
		policy          accessPolicyModule
		status          int
	}{
		{name: "auth", body: `{"model":"model","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, status: 401},
		{name: "model", key: "gateway-test-key", policy: accessPolicyModule{models: []string{"other"}}, body: `{"model":"model","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, status: 403},
		{name: "tool", key: "gateway-test-key", policy: accessPolicyModule{models: []string{"*"}, tools: []string{"safe"}}, body: `{"model":"model","max_tokens":10,"tools":[{"name":"denied","input_schema":{}}],"messages":[{"role":"user","content":"hi"}]}`, status: 403},
		{name: "tpm", key: "gateway-test-key", policy: accessPolicyModule{models: []string{"*"}, tpm: 5}, body: `{"model":"model","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, status: 429},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := &fallbackChatProvider{}
			handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{test.policy}}), upstream))
			response := nativeMessageCall(handler, test.body, test.key)
			if response.Code != test.status || upstream.calls != 0 || !strings.Contains(response.Body.String(), `"type":"error"`) {
				t.Fatalf("response %d %s calls %d", response.Code, response.Body.String(), upstream.calls)
			}
		})
	}
}
func TestMessagesRejectsUnsupportedInputBeforeInference(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled"}}`,
		`{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral","ttl":"30m"}}]}]}`,
		`{"model":"m","max_tokens":10,"messages":[{"role":"assistant","content":"   "}]}`,
		`{"model":"m","max_tokens":10,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call","name":"lookup","input":{}}]}]}`,
		`{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"unknown","content":"x"}]}]}`,
		`{"model":"m","max_tokens":0,"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"m","max_tokens":10,"messages":[]} {}`,
		`{"model":"m","max_tokens":10,"metadata":{"user_id":"ok","extra":"no"},"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"m","max_tokens":10,"metadata":{"user_id":"` + strings.Repeat("я", 513) + `"},"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"m","max_tokens":10,"output_config":{"effort":"minimal"},"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"m","max_tokens":10,"output_config":{"format":{"type":"json_schema"}},"messages":[{"role":"user","content":"hi"}]}`,
	} {
		upstream := &fallbackChatProvider{}
		handler := Routes(NewHandler(modules.NewPipeline(nil), upstream))
		response := nativeMessageCall(handler, body, "")
		if response.Code != 400 || upstream.calls != 0 {
			t.Fatalf("invalid input accepted: %d %s", response.Code, response.Body.String())
		}
	}
}

func TestMessagesConvertsAssistantPrefill(t *testing.T) {
	upstream := &fallbackChatProvider{response: openai.ChatCompletionResponse{
		ID: "message", Model: "model", Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", Content: "B)"}, FinishReason: "stop"}},
		Usage: openai.Usage{PromptTokens: 8, CompletionTokens: 1, TotalTokens: 9},
	}}
	handler := Routes(NewHandler(modules.NewPipeline(nil), upstream))
	response := nativeMessageCall(handler, `{"model":"model","max_tokens":1,"messages":[{"role":"user","content":"Choose A or B"},{"role":"assistant","content":"The answer is ("}]}`, "")
	if response.Code != http.StatusOK || upstream.calls != 1 || len(upstream.request.Request.Messages) != 2 {
		t.Fatalf("response=%d body=%s calls=%d request=%+v", response.Code, response.Body.String(), upstream.calls, upstream.request.Request)
	}
	prefix := upstream.request.Request.Messages[1].Prefix
	if prefix == nil || !*prefix || !strings.Contains(response.Body.String(), `"text":"B)"`) {
		t.Fatalf("prefix=%v response=%s", prefix, response.Body.String())
	}
}

func TestMessagesConvertsPromptCacheControls(t *testing.T) {
	upstream := &fallbackChatProvider{response: openai.ChatCompletionResponse{
		ID: "message", Model: "model", Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
		Usage: openai.Usage{PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11},
	}}
	handler := Routes(NewHandler(modules.NewPipeline(nil), upstream))
	response := nativeMessageCall(handler, `{"model":"model","max_tokens":10,"system":[{"type":"text","text":"rules","cache_control":{"type":"ephemeral","ttl":"1h"}}],"tools":[{"name":"lookup","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"question","cache_control":{"type":"ephemeral","ttl":"5m"}}]}]}`, "")
	if response.Code != http.StatusOK || upstream.calls != 1 {
		t.Fatalf("response=%d body=%s calls=%d", response.Code, response.Body.String(), upstream.calls)
	}
	request := upstream.request.Request
	if count, message := openai.ChatRequestPromptCacheBreakpoints(request); count != 3 || message != "" {
		t.Fatalf("breakpoints count=%d message=%q request=%+v", count, message, request)
	}
	systemPart := request.Messages[0].Content.([]any)[0].(map[string]any)["prompt_cache_breakpoint"].(map[string]any)
	userPart := request.Messages[1].Content.([]any)[0].(map[string]any)["prompt_cache_breakpoint"].(map[string]any)
	toolBreakpoint := request.Tools[0].Function.PromptCacheBreakpoint
	if systemPart["ttl"] != "1h" || userPart["ttl"] != "5m" || toolBreakpoint == nil || toolBreakpoint.Mode != "explicit" {
		t.Fatalf("cache controls changed: system=%v user=%v tool=%+v", systemPart, userPart, toolBreakpoint)
	}
}

type nativeMessagesStreamProvider struct {
	chatProvider
	fail  bool
	calls int
}

func (p *nativeMessagesStreamProvider) StreamChatCompletions(ctx context.Context, request modules.RequestContext, write provider.ChatCompletionStreamWriter) (openai.ChatCompletionResponse, bool, error) {
	p.calls++
	p.request = request
	for _, payload := range []string{
		`{"id":"msg-test","model":"model","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"tool-a","type":"function","function":{"name":"weather","arguments":"{\"city\":"}}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Paris\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15,"completion_tokens_details":{"reasoning_tokens":2}}}`,
	} {
		if err := write(payload); err != nil {
			return openai.ChatCompletionResponse{}, true, err
		}
		if p.fail {
			return openai.ChatCompletionResponse{}, true, errors.New("sensitive upstream error")
		}
	}
	return openai.ChatCompletionResponse{Usage: openai.Usage{PromptTokens: 12, CompletionTokens: 3, TotalTokens: 15, CompletionTokensDetails: &openai.CompletionTokenDetails{ReasoningTokens: 2}}}, true, nil
}
func TestMessagesNativeSSE(t *testing.T) {
	for _, fail := range []bool{false, true} {
		upstream := &nativeMessagesStreamProvider{fail: fail}
		handler := Routes(NewHandler(modules.NewPipeline(nil), upstream))
		response := nativeMessageCall(handler, `{"model":"model","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, "")
		body := response.Body.String()
		if response.Code != 200 || upstream.calls != 1 || !strings.Contains(body, "event: message_start") || strings.Contains(body, "[DONE]") || strings.Contains(body, "sensitive upstream error") {
			t.Fatalf("stream: %d %s", response.Code, body)
		}
		if fail {
			if !strings.Contains(body, "event: error") || strings.Contains(body, "event: message_stop") {
				t.Fatalf("false completion: %s", body)
			}
			continue
		}
		for _, want := range []string{"event: content_block_start", "event: content_block_delta", "event: content_block_stop", "event: message_delta", "event: message_stop", `"stop_reason":"tool_use"`, `"output_tokens":3`} {
			if !strings.Contains(body, want) {
				t.Fatalf("missing %s: %s", want, body)
			}
		}
	}
}
func TestMessagesFallbackSSE(t *testing.T) {
	handler := Routes(NewHandler(modules.NewPipeline(nil), &chatProvider{}))
	response := nativeMessageCall(handler, `{"model":"model","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "event: message_stop") || !strings.Contains(response.Body.String(), `"text":"hello stream"`) {
		t.Fatalf("fallback: %s", response.Body.String())
	}
}

type messagesUsageRecorder struct {
	usage openai.Usage
	calls int
}

func (*messagesUsageRecorder) Name() string                                          { return "billing" }
func (*messagesUsageRecorder) Required() bool                                        { return true }
func (*messagesUsageRecorder) Handle(context.Context, *modules.RequestContext) error { return nil }
func (*messagesUsageRecorder) PostResponseEnabled() bool                             { return true }
func (m *messagesUsageRecorder) HandlePostResponse(_ context.Context, request *modules.RequestContext) error {
	m.calls++
	if request.Response != nil {
		m.usage = request.Response.Usage
	}
	return nil
}

func TestMessagesRouterRetainsBillingUsage(t *testing.T) {
	upstreamCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		if r.Header.Get("Authorization") != "Bearer upstream-test-key" {
			t.Error("gateway credential leaked or upstream credential lost")
		}
		var request struct {
			MaxTokens int  `json:"max_tokens"`
			Stream    bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.MaxTokens != 10 || !request.Stream {
			t.Error("native request limits or streaming lost")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"id-test\",\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3,\"total_tokens\":15,\"completion_tokens_details\":{\"reasoning_tokens\":2}}}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	recorder := &messagesUsageRecorder{}
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "native-test", Type: "openai-compatible", BaseURL: server.URL, APIKey: "upstream-test-key", Stream: true, Models: []string{"model"}, Capabilities: []string{"chat", "stream"}}}, Modules: modules.NewPipeline([]modules.Module{recorder})})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"*"}}}}), router))
	response := nativeMessageCall(handler, `{"model":"model","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, "gateway-test-key")
	if response.Code != 200 || upstreamCalls != 1 || recorder.calls != 1 || recorder.usage.TotalTokens != 15 || recorder.usage.CompletionTokensDetails == nil || recorder.usage.CompletionTokensDetails.ReasoningTokens != 2 || !strings.Contains(response.Body.String(), `"output_tokens":3`) || !strings.Contains(response.Body.String(), `"thinking_tokens":2`) {
		t.Fatalf("billing: code=%d calls=%d usage=%+v body=%s", response.Code, recorder.calls, recorder.usage, response.Body.String())
	}
}

func TestMessagesWriterRejectsTruncatedAndOversizedStreams(t *testing.T) {
	response := httptest.NewRecorder()
	writer := &messagesWriter{destination: response, headers: make(http.Header), tools: map[int]int{}}
	if err := writer.chunk(`{"id":"m","choices":[{"index":0,"delta":{"content":"hello"}}]}`); err != nil {
		t.Fatal(err)
	}
	if err := writer.chunk("[DONE]"); err == nil {
		t.Fatal("truncated stream accepted")
	}
	writer.finish()
	if strings.Contains(response.Body.String(), "event: message_stop") || !strings.Contains(response.Body.String(), "event: error") {
		t.Fatal(response.Body.String())
	}
	bounded := &messagesWriter{destination: httptest.NewRecorder(), headers: make(http.Header)}
	if _, err := bounded.Write(make([]byte, (32<<20)+1)); err == nil {
		t.Fatal("oversized response accepted")
	}
}

func TestMessagesImageAndTextOrder(t *testing.T) {
	var request messagesRequest
	if err := json.Unmarshal([]byte(`{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"text","text":"before"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}},{"type":"text","text":"after"}]}]}`), &request); err != nil {
		t.Fatal(err)
	}
	chat, err := request.chat()
	if err != nil {
		t.Fatal(err)
	}
	parts := chat.Messages[0].Content.([]any)
	if len(parts) != 3 || parts[0].(map[string]any)["text"] != "before" || parts[1].(map[string]any)["type"] != "image_url" || parts[2].(map[string]any)["text"] != "after" {
		t.Fatalf("image order lost: %+v", parts)
	}
}
func TestMessagesRejectsInvalidCompletedToolJSON(t *testing.T) {
	writer := &messagesWriter{destination: httptest.NewRecorder(), headers: make(http.Header), tools: map[int]int{}}
	if err := writer.chunk(`{"id":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"f","arguments":"{"}}]},"finish_reason":"tool_calls"}]}`); err != nil {
		t.Fatal(err)
	}
	if err := writer.chunk("[DONE]"); err == nil {
		t.Fatal("invalid JSON tool input accepted")
	}
}

type messagesFailWriter struct {
	http.ResponseWriter
	failure error
}

func (w messagesFailWriter) Write([]byte) (int, error) { return 0, w.failure }
func TestMessagesPropagatesClientWriteFailure(t *testing.T) {
	failure := errors.New("client disconnected")
	writer := &messagesWriter{destination: messagesFailWriter{httptest.NewRecorder(), failure}, headers: make(http.Header), tools: map[int]int{}}
	writer.Header().Set("Content-Type", "text/event-stream")
	if _, err := writer.Write([]byte("data: {\"id\":\"m\",\"choices\":[]}\n\n")); !errors.Is(err, failure) {
		t.Fatalf("write failure lost: %v", err)
	}
}

func TestMessagesAnthropicStreamFinalUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "upstream-test-key" {
			t.Error("native authentication or path lost")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"message\":{\"id\":\"msg-test\",\"model\":\"model\",\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":2}}}\n\nevent: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\nevent: message_stop\ndata: {}\n\n"))
	}))
	defer server.Close()
	recorder := &messagesUsageRecorder{}
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "native-test", Type: "anthropic", BaseURL: server.URL, APIKey: "upstream-test-key", Stream: true, Models: []string{"model"}, Capabilities: []string{"chat", "stream"}}}, Modules: modules.NewPipeline([]modules.Module{recorder})})
	response := nativeMessageCall(Routes(NewHandler(modules.NewPipeline(nil), router)), `{"model":"model","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, "")
	if response.Code != 200 || recorder.calls != 1 || recorder.usage.TotalTokens != 15 || !strings.Contains(response.Body.String(), `"output_tokens":3`) || !strings.Contains(response.Body.String(), `"input_tokens":10`) || !strings.Contains(response.Body.String(), "event: message_stop") {
		t.Fatalf("native accounting: code=%d usage=%+v body=%s", response.Code, recorder.usage, response.Body.String())
	}
}

func TestMessagesPreservesMatchedStopInJSONAndFallbackStream(t *testing.T) {
	sequence := " END "
	for _, stream := range []string{"false", "true"} {
		upstream := &fallbackChatProvider{response: openai.ChatCompletionResponse{ID: "m", Model: "model", Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", Content: "hello"}, FinishReason: "stop", StopSequence: &sequence}}}}
		response := nativeMessageCall(Routes(NewHandler(modules.NewPipeline(nil), upstream)), `{"model":"model","max_tokens":10,"stream":`+stream+`,"messages":[{"role":"user","content":"hi"}]}`, "")
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"stop_reason":"stop_sequence"`) || !strings.Contains(response.Body.String(), `"stop_sequence":" END "`) {
			t.Fatalf("matched stop lost: %s", response.Body.String())
		}
	}
}

func TestMessagesStopSequencesThroughAnthropic(t *testing.T) {
	for _, stream := range []string{"false", "true"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			stops, ok := body["stop_sequences"].([]any)
			if !ok || len(stops) != 1 || stops[0] != " END " {
				t.Errorf("stop request lost: %v", body)
			}
			if stream == "false" {
				_, _ = w.Write([]byte(`{"id":"m","model":"model","content":[{"type":"text","text":"hello"}],"stop_reason":"stop_sequence","stop_sequence":" END ","usage":{"input_tokens":2,"output_tokens":1}}`))
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message_start\ndata: {\"message\":{\"id\":\"m\",\"model\":\"model\",\"usage\":{\"input_tokens\":2}}}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"stop_sequence\",\"stop_sequence\":\" END \"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {}\n\n"))
		}))
		router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "native", Type: "anthropic", BaseURL: server.URL, Models: []string{"model"}, Stream: true}}})
		response := nativeMessageCall(Routes(NewHandler(modules.NewPipeline(nil), router)), `{"model":"model","max_tokens":10,"stop_sequences":[" END "],"stream":`+stream+`,"messages":[{"role":"user","content":"hi"}]}`, "")
		server.Close()
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"stop_reason":"stop_sequence"`) || !strings.Contains(response.Body.String(), `"stop_sequence":" END "`) {
			t.Fatalf("stop protocol: %d %s", response.Code, response.Body.String())
		}
	}
}
