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
