package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type rewriteContextModule struct{ rewrite func(*modules.RequestContext) }

func (rewriteContextModule) Name() string   { return "rewrite-context" }
func (rewriteContextModule) Required() bool { return true }
func (m rewriteContextModule) Handle(_ context.Context, req *modules.RequestContext) error {
	m.rewrite(req)
	return nil
}

func TestAdmissionUsesEffectiveChatAndCountContext(t *testing.T) {
	for _, endpoint := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/messages/count_tokens", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`},
		{"/v1beta/models/m:countTokens", `{"contents":[{"parts":[{"text":"hi"}]}]}`},
	} {
		for _, tc := range []struct {
			name    string
			code    int
			rewrite func(*modules.RequestContext)
		}{
			{"model", 403, func(req *modules.RequestContext) { req.Request.Model = "forbidden" }},
			{"tools", 403, func(req *modules.RequestContext) {
				req.Request.Tools = []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "forbidden", Parameters: map[string]any{"type": "object"}}}}
			}},
			{"tokens", 429, func(req *modules.RequestContext) {
				req.Request.Messages = []openai.Message{{Role: "user", Content: strings.Repeat("large input ", 1000)}}
			}},
		} {
			t.Run(endpoint.path+"/"+tc.name, func(t *testing.T) {
				spy := &countProviderSpy{}
				handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"m"}, tools: []string{"safe"}, tpm: 100}}, rewriteContextModule{tc.rewrite}}), spy))
				request := httptest.NewRequest(http.MethodPost, endpoint.path, strings.NewReader(endpoint.body))
				request.Header.Set("Authorization", "Bearer gateway-test-key")
				request.Header.Set("anthropic-version", "2023-06-01")
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != tc.code || spy.calls != 0 {
					t.Fatalf("effective %s bypassed: want %d got %d %s", tc.name, tc.code, response.Code, response.Body.String())
				}
			})
		}
	}
}

func TestChatRejectsOptionsInvalidatedByPipeline(t *testing.T) {
	provider := &chatProvider{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{rewriteContextModule{rewrite: func(req *modules.RequestContext) {
		limit := 0
		req.Request.MaxCompletionTokens = &limit
	}}}), provider)
	response := httptest.NewRecorder()
	handler.ChatCompletions(response, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)))
	if response.Code != http.StatusBadGateway || provider.request.RequestID != "" || !strings.Contains(response.Body.String(), `"module_failed"`) || !strings.Contains(response.Body.String(), "output token limit") {
		t.Fatalf("invalid pipeline output continued: status=%d provider=%+v body=%s", response.Code, provider.request, response.Body.String())
	}
}

func TestCompletionsRejectsOptionsInvalidatedByPipeline(t *testing.T) {
	provider := &chatProvider{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{rewriteContextModule{rewrite: func(req *modules.RequestContext) {
		limit := -1
		req.CompletionRequest.MaxTokens = &limit
	}}}), provider)
	response := httptest.NewRecorder()
	handler.Completions(response, httptest.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader(`{"model":"m","prompt":"hello"}`)))
	if response.Code != http.StatusBadGateway || provider.request.RequestID != "" || !strings.Contains(response.Body.String(), `"module_failed"`) || !strings.Contains(response.Body.String(), "max_tokens") {
		t.Fatalf("invalid pipeline output continued: status=%d provider=%+v body=%s", response.Code, provider.request, response.Body.String())
	}
}

func TestEmbeddingsRejectsOptionsInvalidatedByPipeline(t *testing.T) {
	provider := &chatProvider{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{rewriteContextModule{rewrite: func(req *modules.RequestContext) {
		dimensions := 0
		req.EmbeddingRequest.Dimensions = &dimensions
	}}}), provider)
	response := httptest.NewRecorder()
	handler.Embeddings(response, httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"m","input":"hello"}`)))
	if response.Code != http.StatusBadGateway || provider.request.RequestID != "" || !strings.Contains(response.Body.String(), `"module_failed"`) || !strings.Contains(response.Body.String(), "dimensions") {
		t.Fatalf("invalid pipeline output continued: status=%d provider=%+v body=%s", response.Code, provider.request, response.Body.String())
	}
}

func TestRerankRejectsOptionsInvalidatedByPipeline(t *testing.T) {
	provider := &chatProvider{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{rewriteContextModule{rewrite: func(req *modules.RequestContext) {
		limit := 0
		req.RerankRequest.MaxChunksPerDoc = &limit
	}}}), provider)
	response := httptest.NewRecorder()
	handler.Rerank(response, httptest.NewRequest(http.MethodPost, "/v1/rerank", strings.NewReader(`{"model":"m","query":"hello","documents":["one"]}`)))
	if response.Code != http.StatusBadGateway || provider.request.RequestID != "" || !strings.Contains(response.Body.String(), `"module_failed"`) || !strings.Contains(response.Body.String(), "max_chunks_per_doc") {
		t.Fatalf("invalid pipeline output continued: status=%d provider=%+v body=%s", response.Code, provider.request, response.Body.String())
	}
}

func TestModerationsRejectsOptionsInvalidatedByPipeline(t *testing.T) {
	provider := &chatProvider{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{rewriteContextModule{rewrite: func(req *modules.RequestContext) {
		req.ModerationRequest.Metadata = map[string]string{strings.Repeat("k", 65): "value"}
	}}}), provider)
	response := httptest.NewRecorder()
	handler.Moderations(response, httptest.NewRequest(http.MethodPost, "/v1/moderations", strings.NewReader(`{"input":"hello"}`)))
	if response.Code != http.StatusBadGateway || provider.request.RequestID != "" || !strings.Contains(response.Body.String(), `"module_failed"`) || !strings.Contains(response.Body.String(), "metadata") {
		t.Fatalf("invalid pipeline output continued: status=%d provider=%+v body=%s", response.Code, provider.request, response.Body.String())
	}
}

func TestResponseCompactionRejectsEnvelopeInvalidatedByPipeline(t *testing.T) {
	provider := &chatProvider{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{rewriteContextModule{rewrite: func(req *modules.RequestContext) {
		req.ResponseRequest.Input = nil
	}}}), provider)
	response := httptest.NewRecorder()
	handler.CompactResponse(response, httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"m","input":"hello"}`)))
	if response.Code != http.StatusBadGateway || provider.request.RequestID != "" || !strings.Contains(response.Body.String(), `"module_failed"`) || !strings.Contains(response.Body.String(), "input") {
		t.Fatalf("invalid pipeline output continued: status=%d provider=%+v body=%s", response.Code, provider.request, response.Body.String())
	}
}

func TestInferenceRejectsAttachmentsInvalidatedByPipeline(t *testing.T) {
	invalidImage := []any{map[string]any{"type": "input_image", "image_url": "https://example.test/image.png"}}
	invalidAudio := []any{map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "invalid", "format": "wav"}}}
	invalidFile := []any{map[string]any{"type": "input_file", "filename": "document.pdf", "file_data": "invalid"}}
	invalidVideo := []any{map[string]any{"type": "input_video", "input_video": map[string]any{"data": "invalid", "format": "mp4"}}}

	for _, test := range []struct {
		name    string
		path    string
		body    string
		rewrite func(*modules.RequestContext)
		input   string
	}{
		{name: "chat image", path: "/v1/chat/completions", body: `{"model":"m","messages":[{"role":"user","content":"hello"}]}`, rewrite: func(req *modules.RequestContext) { req.Request.Messages[0].Content = invalidImage }, input: "image"},
		{name: "chat audio", path: "/v1/chat/completions", body: `{"model":"m","messages":[{"role":"user","content":"hello"}]}`, rewrite: func(req *modules.RequestContext) { req.Request.Messages[0].Content = invalidAudio }, input: "audio"},
		{name: "chat file", path: "/v1/chat/completions", body: `{"model":"m","messages":[{"role":"user","content":"hello"}]}`, rewrite: func(req *modules.RequestContext) { req.Request.Messages[0].Content = invalidFile }, input: "file"},
		{name: "chat video", path: "/v1/chat/completions", body: `{"model":"m","messages":[{"role":"user","content":"hello"}]}`, rewrite: func(req *modules.RequestContext) { req.Request.Messages[0].Content = invalidVideo }, input: "video"},
		{name: "responses image", path: "/v1/responses", body: `{"model":"m","input":"hello"}`, rewrite: func(req *modules.RequestContext) { req.ResponseRequest.Input = invalidImage }, input: "image"},
		{name: "responses audio", path: "/v1/responses", body: `{"model":"m","input":"hello"}`, rewrite: func(req *modules.RequestContext) { req.ResponseRequest.Input = invalidAudio }, input: "audio"},
		{name: "responses file", path: "/v1/responses", body: `{"model":"m","input":"hello"}`, rewrite: func(req *modules.RequestContext) { req.ResponseRequest.Input = invalidFile }, input: "file"},
		{name: "token count image", path: "/v1/responses/input_tokens", body: `{"model":"m","input":"hello"}`, rewrite: func(req *modules.RequestContext) { req.ResponseRequest.Input = invalidImage }, input: "image"},
		{name: "token count audio", path: "/v1/responses/input_tokens", body: `{"model":"m","input":"hello"}`, rewrite: func(req *modules.RequestContext) { req.ResponseRequest.Input = invalidAudio }, input: "audio"},
		{name: "token count file", path: "/v1/responses/input_tokens", body: `{"model":"m","input":"hello"}`, rewrite: func(req *modules.RequestContext) { req.ResponseRequest.Input = invalidFile }, input: "file"},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := &responseInputTokenCountProvider{}
			handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{rewriteContextModule{rewrite: test.rewrite}}), upstream))
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			request.Header.Set("anthropic-version", "2023-06-01")
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadGateway || upstream.calls != 0 || !strings.Contains(response.Body.String(), `"code":"module_failed"`) || !strings.Contains(response.Body.String(), "invalid "+test.input+" input") {
				t.Fatalf("invalid module attachment continued: status=%d count_calls=%d body=%s", response.Code, upstream.calls, response.Body.String())
			}
		})
	}

	for kind, invalid := range map[string]any{"image": invalidImage, "audio": invalidAudio, "file": invalidFile, "video": invalidVideo} {
		t.Run("message count "+kind, func(t *testing.T) {
			upstream := &countProviderSpy{}
			handler := NewHandler(modules.NewPipeline([]modules.Module{rewriteContextModule{rewrite: func(req *modules.RequestContext) {
				req.Request.Messages[0].Content = invalid
			}}}), upstream)
			response := httptest.NewRecorder()
			_, ok := handler.countContextTokens(response, httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil), openai.ChatCompletionRequest{Model: "m", Messages: []openai.Message{{Role: "user", Content: "hello"}}}, "")
			if ok || response.Code != http.StatusBadGateway || upstream.calls != 0 || !strings.Contains(response.Body.String(), `"code":"module_failed"`) || !strings.Contains(response.Body.String(), "invalid "+kind+" input") {
				t.Fatalf("invalid module attachment continued: ok=%t status=%d calls=%d body=%s", ok, response.Code, upstream.calls, response.Body.String())
			}
		})
	}
}

func TestAdmissionUsesReplacedTypedRequests(t *testing.T) {
	for _, endpoint := range []struct{ path, body string }{
		{"/v1/responses", `{"model":"m","input":"hi","max_output_tokens":1}`},
		{"/v1/embeddings", `{"model":"m","input":"hi"}`},
		{"/v1/rerank", `{"model":"m","query":"hi","documents":["one"]}`},
		{"/v1/moderations", `{"model":"m","input":"hi"}`},
	} {
		for _, change := range []string{"model", "tokens", "nil"} {
			t.Run(endpoint.path+"/"+change, func(t *testing.T) {
				code := 403
				if change == "tokens" {
					code = 429
				}
				if change == "nil" {
					code = 502
				}
				rewrite := func(req *modules.RequestContext) {
					if req.ResponseRequest != nil {
						replacement := *req.ResponseRequest
						if change == "model" {
							replacement.Model = "forbidden"
						} else {
							replacement.Input = strings.Repeat("large context ", 1000)
						}
						req.ResponseRequest = &replacement
						if change == "nil" {
							req.ResponseRequest = nil
						}
					}
					if req.EmbeddingRequest != nil {
						replacement := *req.EmbeddingRequest
						if change == "model" {
							replacement.Model = "forbidden"
						} else {
							replacement.Input = strings.Repeat("large context ", 1000)
						}
						req.EmbeddingRequest = &replacement
						if change == "nil" {
							req.EmbeddingRequest = nil
						}
					}
					if req.RerankRequest != nil {
						replacement := *req.RerankRequest
						if change == "model" {
							replacement.Model = "forbidden"
						} else {
							replacement.Query = strings.Repeat("large context ", 1000)
						}
						req.RerankRequest = &replacement
						if change == "nil" {
							req.RerankRequest = nil
						}
					}
					if req.ModerationRequest != nil {
						replacement := *req.ModerationRequest
						if change == "model" {
							replacement.Model = "forbidden"
						} else {
							replacement.Input = strings.Repeat("large context ", 1000)
						}
						req.ModerationRequest = &replacement
						if change == "nil" {
							req.ModerationRequest = nil
						}
					}
				}
				spy := &chatProvider{}
				handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"m"}, tpm: 100}}, rewriteContextModule{rewrite}}), spy))
				request := httptest.NewRequest(http.MethodPost, endpoint.path, strings.NewReader(endpoint.body))
				request.Header.Set("Authorization", "Bearer gateway-test-key")
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != code || spy.request.RequestID != "" {
					t.Fatalf("effective request bypassed: want %d got %d %s", code, response.Code, response.Body.String())
				}
			})
		}
	}
}
