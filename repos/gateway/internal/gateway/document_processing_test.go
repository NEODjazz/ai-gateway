package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

type gatewayDocumentConverter struct{ calls int }

func (f *gatewayDocumentConverter) Convert(context.Context, openai.ResponseFileAttachment, string) (string, error) {
	f.calls++
	return strings.Repeat("extracted words ", 80), nil
}

type documentPolicyProbe struct {
	name   string
	calls  int
	tokens int
	t      *testing.T
}

func (m *documentPolicyProbe) Name() string { return m.name }
func (*documentPolicyProbe) Required() bool { return true }
func (m *documentPolicyProbe) Handle(_ context.Context, req *modules.RequestContext) error {
	m.calls++
	payload, _ := json.Marshal(req.Request)
	if req.ResponseRequest != nil {
		payload, _ = json.Marshal(req.ResponseRequest)
	}
	if strings.Contains(string(payload), "file_data") || !strings.Contains(string(payload), "extracted words") {
		m.t.Fatal("security or billing saw opaque file")
	}
	m.tokens = openai.ChatInputTokens(req.Request)
	if req.ResponseRequest != nil {
		m.tokens = openai.ResponseInputTokens(*req.ResponseRequest)
	}
	return nil
}

func TestDocumentProcessingBeforeSecurityAccountingAndOllama(t *testing.T) {
	for _, responses := range []bool{false, true} {
		t.Run(map[bool]string{false: "chat", true: "responses"}[responses], func(t *testing.T) {
			upstreamCalls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamCalls++
				if r.URL.Path == "/v1/responses" {
					var responseRequest openai.ResponseRequest
					if err := json.NewDecoder(r.Body).Decode(&responseRequest); err != nil {
						t.Fatal(err)
					}
					payload, _ := json.Marshal(responseRequest.Input)
					if !strings.Contains(string(payload), "extracted words") || strings.Contains(string(payload), "file_data") {
						t.Fatal("Responses did not receive text")
					}
					_, _ = w.Write([]byte(`{"id":"resp_test","object":"response","status":"completed","model":"qwen","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":400,"output_tokens":1,"total_tokens":401}}`))
					return
				}

				var request struct{ Messages []struct{ Content string } }
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				if len(request.Messages) == 0 || !strings.Contains(request.Messages[0].Content, "extracted words") {
					t.Fatalf("upstream=%+v", request)
				}
				_, _ = w.Write([]byte(`{"model":"qwen","message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":400,"eval_count":1}`))
			}))
			t.Cleanup(upstream.Close)
			converter := &gatewayDocumentConverter{}
			security := &documentPolicyProbe{name: "anonymizer", t: t}
			billing := &documentPolicyProbe{name: "billing", t: t}
			llm := provider.New(provider.Config{DocumentConverter: converter, Modules: modules.NewPipeline([]modules.Module{security, billing}), Endpoints: []config.ProviderEndpointConfig{{Name: "local", Type: "ollama", BaseURL: upstream.URL, Models: []string{"qwen"}, Capabilities: []string{"chat", "responses"}, DocumentProcessing: "docling"}}})
			h := NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: []string{"qwen"}}}), llm)
			file := map[string]any{"type": "input_file", "filename": "test.pdf", "file_data": "data:application/pdf;base64," + base64.StdEncoding.EncodeToString([]byte("%PDF-test"))}
			body := map[string]any{"model": "qwen", "messages": []any{map[string]any{"role": "user", "content": []any{file}}}}
			path := "/v1/chat/completions"
			if responses {
				path = "/v1/responses"
				delete(body, "messages")
				body["input"] = []any{map[string]any{"role": "user", "content": []any{file}}}
			}
			encoded, _ := json.Marshal(body)
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(encoded)))
			w := httptest.NewRecorder()
			if responses {
				h.Responses(w, request)
			} else {
				h.ChatCompletions(w, request)
			}
			if w.Code != 200 || converter.calls != 1 || upstreamCalls != 1 || security.calls != 1 || billing.calls != 1 || billing.tokens < 200 {
				t.Fatalf("status=%d body=%s converter=%d upstream=%d security=%d billing=%d tokens=%d", w.Code, w.Body.String(), converter.calls, upstreamCalls, security.calls, billing.calls, billing.tokens)
			}
			// Denied models cannot enqueue expensive conversion work.
			h = NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: []string{"other"}}}), llm)
			request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(encoded)))
			w = httptest.NewRecorder()
			if responses {
				h.Responses(w, request)
			} else {
				h.ChatCompletions(w, request)
			}
			if w.Code != 403 || converter.calls != 1 || upstreamCalls != 1 {
				t.Fatalf("unauthorized conversion: status=%d calls=%d", w.Code, converter.calls)
			}
		})
	}
}
