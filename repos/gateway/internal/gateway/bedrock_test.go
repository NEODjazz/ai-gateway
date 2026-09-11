package gateway

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

func TestBedrockConverseUsesChatPolicyRoutingAndBilling(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprint(w, `{"id":"chat_1","object":"chat.completion","model":"upstream","choices":[{"index":0,"message":{"role":"assistant","content":"sunny","tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}`)
	}))
	defer upstream.Close()
	recorder := &messagesUsageRecorder{}
	router := provider.New(provider.Config{
		Endpoints: []config.ProviderEndpointConfig{{Name: "deployment", Type: "openai-compatible", BaseURL: upstream.URL, Models: []string{"public"}, ModelAliases: map[string]string{"public": "upstream"}, Capabilities: []string{"chat", "tools"}}},
		Modules:   modules.NewPipeline([]modules.Module{recorder}),
	})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"public"}, tools: []string{"weather"}}}}), router))
	body := `{"messages":[{"role":"user","content":[{"text":"weather"}]}],"toolConfig":{"tools":[{"toolSpec":{"name":"weather","inputSchema":{"json":{"type":"object"}}}}]},"inferenceConfig":{"maxTokens":32}}`
	request := httptest.NewRequest(http.MethodPost, "/model/public/converse?provider=deployment", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer gateway-test-key")
	request.Header.Set("X-Request-ID", "bedrock-converse-external")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("X-Execution-ID") == "" || !strings.Contains(response.Body.String(), `"stopReason":"tool_use"`) || !strings.Contains(response.Body.String(), `"totalTokens":12`) || !strings.Contains(response.Body.String(), `"toolUseId":"call_1"`) {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if calls.Load() != 1 || recorder.calls != 1 || recorder.usage.TotalTokens != 12 {
		t.Fatalf("calls=%d billing_calls=%d usage=%+v", calls.Load(), recorder.calls, recorder.usage)
	}
}

func TestBedrockInvokeUsesPolicyRoutingAndBilling(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.EscapedPath() != "/model/upstream/invoke" {
			t.Fatalf("path=%q", r.URL.EscapedPath())
		}
		_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"upstream","content":[{"type":"text","text":"sunny"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":7,"output_tokens":5}}`)
	}))
	defer upstream.Close()
	recorder := &messagesUsageRecorder{}
	router := provider.New(provider.Config{
		Endpoints: []config.ProviderEndpointConfig{{Name: "deployment", Type: "bedrock", BaseURL: upstream.URL, APIKey: "provider-key", Models: []string{"public"}, ModelAliases: map[string]string{"public": "upstream"}, Capabilities: []string{"chat", "bedrock_invoke"}}},
		Modules:   modules.NewPipeline([]modules.Module{recorder}),
	})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"public"}}}}), router))
	request := httptest.NewRequest(http.MethodPost, "/model/public/invoke?provider=deployment", strings.NewReader(`{"anthropic_version":"bedrock-2023-05-31","max_tokens":32,"messages":[{"role":"user","content":"weather"}]}`))
	request.Header.Set("Authorization", "Bearer gateway-test-key")
	request.Header.Set("X-Request-ID", "bedrock-invoke-external")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("X-Execution-ID") == "" || !strings.Contains(response.Body.String(), `"type":"message"`) || !strings.Contains(response.Body.String(), `"input_tokens":7`) {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if calls.Load() != 1 || recorder.calls != 1 || recorder.usage.TotalTokens != 12 {
		t.Fatalf("calls=%d billing_calls=%d usage=%+v", calls.Load(), recorder.calls, recorder.usage)
	}
}

func TestBedrockInvokeRejectsInvalidContractBeforeExecution(t *testing.T) {
	for _, body := range []string{
		`{"anthropic_version":"2023-06-01","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`,
		`{"anthropic_version":"bedrock-2023-05-31","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":true}`,
		`{"anthropic_version":"bedrock-2023-05-31","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"output_config":{"effort":"high"}}`,
	} {
		upstream := &chatProvider{}
		response := httptest.NewRecorder()
		Routes(NewHandler(modules.NewPipeline(nil), upstream)).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/model/model/invoke", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest || upstream.request.Request.Model != "" {
			t.Fatalf("status=%d request=%+v body=%s", response.Code, upstream.request.Request, response.Body.String())
		}
	}
}

func TestBedrockInvokeAcceptsStructuredOutput(t *testing.T) {
	upstream := &chatProvider{}
	response := httptest.NewRecorder()
	body := `{"anthropic_version":"bedrock-2023-05-31","max_tokens":32,"messages":[{"role":"user","content":"answer"}],"output_config":{"format":{"type":"json_schema","schema":{"type":"object","required":["answer"],"properties":{"answer":{"type":"string"}}}}}}`
	Routes(NewHandler(modules.NewPipeline(nil), upstream)).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/model/model/invoke", strings.NewReader(body)))
	format := upstream.request.Request.ResponseFormat
	if response.Code != http.StatusOK || format == nil || format.Type != "json_schema" || format.JSONSchema == nil || format.JSONSchema.Schema == nil {
		t.Fatalf("status=%d body=%s request=%+v", response.Code, response.Body.String(), upstream.request.Request)
	}
}

func TestBedrockConverseAcceptsBoundedNativeImage(t *testing.T) {
	upstream := &chatProvider{}
	handler := Routes(NewHandler(modules.NewPipeline(nil), upstream))
	data := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nimage"))
	body := `{"messages":[{"role":"user","content":[{"text":"describe"},{"image":{"format":"png","source":{"bytes":"` + data + `"}}}]}]}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/model/model/converse", strings.NewReader(body)))
	attachments, err := openai.ChatImageAttachments(upstream.request.Request.Messages)
	if response.Code != http.StatusOK || err != nil || len(attachments) != 1 || attachments[0].MediaType != "image/png" {
		t.Fatalf("status=%d body=%s attachments=%+v err=%v", response.Code, response.Body.String(), attachments, err)
	}
}

func TestBedrockConverseAcceptsBoundedNativeDocument(t *testing.T) {
	upstream := &chatProvider{}
	handler := Routes(NewHandler(modules.NewPipeline(nil), upstream))
	data := base64.StdEncoding.EncodeToString([]byte("%PDF-test"))
	body := `{"messages":[{"role":"user","content":[{"text":"summarize"},{"document":{"format":"pdf","name":"Report","source":{"bytes":"` + data + `"}}}]}]}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/model/model/converse", strings.NewReader(body)))
	documents, err := openai.BedrockDocumentAttachments(upstream.request.Request.Messages)
	if response.Code != http.StatusOK || err != nil || len(documents) != 1 || documents[0].MediaType != "application/pdf" || upstream.request.Request.NativeInputTokens != len([]byte("%PDF-test")) {
		t.Fatalf("status=%d body=%s documents=%+v reserve=%d err=%v", response.Code, response.Body.String(), documents, upstream.request.Request.NativeInputTokens, err)
	}
}

func TestBedrockConverseRejectsUnknownFieldsBeforeExecution(t *testing.T) {
	response := httptest.NewRecorder()
	Routes(Handler{}).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/model/model/converse", strings.NewReader(`{"messages":[{"role":"user","content":[{"text":"hello"}]}],"unknownField":{"top_k":1}}`)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_request"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestBedrockConverseAcceptsAdditionalModelRequestFields(t *testing.T) {
	upstream := &chatProvider{}
	handler := Routes(NewHandler(modules.NewPipeline(nil), upstream))
	body := `{"messages":[{"role":"user","content":[{"text":"hello"}]}],"additionalModelRequestFields":{"top_k":42}}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/model/model/converse", strings.NewReader(body)))
	if response.Code != http.StatusOK || string(upstream.request.Request.BedrockAdditionalModelRequestFields) != `{"top_k":42}` || upstream.request.Request.NativeInputTokens <= 0 {
		t.Fatalf("status=%d body=%s request=%+v", response.Code, response.Body.String(), upstream.request.Request)
	}
}

func TestBedrockConverseAcceptsGuardrailConfig(t *testing.T) {
	upstream := &chatProvider{}
	handler := Routes(NewHandler(modules.NewPipeline(nil), upstream))
	body := `{"messages":[{"role":"user","content":[{"text":"hello"}]}],"guardrailConfig":{"guardrailIdentifier":"guardrail123","guardrailVersion":"1","trace":"enabled"}}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/model/model/converse", strings.NewReader(body)))
	config := upstream.request.Request.BedrockGuardrailConfig
	if response.Code != http.StatusOK || config == nil || config.GuardrailIdentifier != "guardrail123" || config.Trace != "enabled" {
		t.Fatalf("status=%d body=%s config=%+v", response.Code, response.Body.String(), config)
	}
}

func TestBedrockConverseRouteAcceptsEscapedARNModel(t *testing.T) {
	handler := Routes(NewHandler(modules.NewPipeline(nil), &chatProvider{}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/model/arn%3Aaws%3Abedrock%3Aus-east-1%3A123%3Aprompt%2Fprompt-id%3A1/converse", strings.NewReader(`{"messages":[{"role":"user","content":[{"text":"hello"}]}]}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
