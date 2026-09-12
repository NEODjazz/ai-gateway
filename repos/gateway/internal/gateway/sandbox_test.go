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

type sandboxGatewayProvider struct {
	*batchProvider
	request modules.RequestContext
}

func (p *sandboxGatewayProvider) ExecuteSandbox(_ context.Context, request modules.RequestContext) (openai.SandboxExecutionResult, error) {
	p.request = request
	return openai.SandboxExecutionResult{Object: "code_execution", Stdout: "42\n", Results: []map[string]any{}}, nil
}

func TestSandboxExecutionUsesAuthPolicyAndBoundedTPMReserve(t *testing.T) {
	upstream := &sandboxGatewayProvider{batchProvider: &batchProvider{models: []string{"code-interpreter"}}}
	rates := &embeddingTokenRateStore{}
	handler := Routes(NewHandlerWithRateLimitStore(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: []string{"code-interpreter"}}}), upstream, rates))
	request := httptest.NewRequest(http.MethodPost, "/v1/sandbox/execute", strings.NewReader(`{"model":"code-interpreter","code":"print(42)","timeout_seconds":20}`))
	request.Header.Set("Authorization", "Bearer test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"stdout":"42\n"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if upstream.request.APIKey != "" || upstream.request.SandboxRequest == nil || upstream.request.SandboxRequest.Language != openai.DefaultSandboxLanguage || upstream.request.SandboxRequest.Template != openai.DefaultSandboxTemplate {
		t.Fatalf("request=%+v sandbox=%+v", upstream.request, upstream.request.SandboxRequest)
	}
	if rates.tokens != upstream.request.SandboxRequest.InputTokens() || rates.tokens < 1 {
		t.Fatalf("TPM=%d want=%d", rates.tokens, upstream.request.SandboxRequest.InputTokens())
	}
}

func TestSandboxExecutionRejectsInvalidRequestsBeforeProvider(t *testing.T) {
	for _, test := range []struct {
		path string
		body string
	}{
		{"/v1/sandbox/execute?debug=true", `{"model":"code-interpreter","code":"x"}`},
		{"/v1/sandbox/execute", `{"model":"code-interpreter","code":""}`},
		{"/v1/sandbox/execute", `{"model":"code-interpreter","code":"x","timeout_seconds":901}`},
		{"/v1/sandbox/execute", `{"model":"code-interpreter","code":"x","unknown":true}`},
	} {
		upstream := &sandboxGatewayProvider{batchProvider: &batchProvider{models: []string{"code-interpreter"}}}
		handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: []string{"code-interpreter"}}}), upstream))
		request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
		request.Header.Set("Authorization", "Bearer test")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || upstream.request.SandboxRequest != nil {
			t.Fatalf("path=%s status=%d body=%s", test.path, response.Code, response.Body.String())
		}
	}
}

func TestSandboxExecutionEnforcesModelAuthorization(t *testing.T) {
	upstream := &sandboxGatewayProvider{batchProvider: &batchProvider{models: []string{"code-interpreter"}}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: []string{"other"}}}), upstream))
	request := httptest.NewRequest(http.MethodPost, "/v1/sandbox/execute", strings.NewReader(`{"model":"code-interpreter","code":"x"}`))
	request.Header.Set("Authorization", "Bearer test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || upstream.request.SandboxRequest != nil {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
