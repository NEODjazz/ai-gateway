package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/containerstate"
	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

type responseInputTokenCountProvider struct {
	chatProvider
	calls int
}

func (p *responseInputTokenCountProvider) CountResponseInputTokens(_ context.Context, req modules.RequestContext) (openai.ResponseInputTokenCount, error) {
	p.calls++
	p.request = req
	return openai.ResponseInputTokenCount{Object: "response.input_tokens", InputTokens: 23}, nil
}

func TestResponseInputTokenCountUsesPolicyWithoutBilling(t *testing.T) {
	counter := &responseInputTokenCountProvider{}
	billing := &lifecycleBillingModule{}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{
		accessPolicyModule{models: []string{"allowed"}, tools: []string{"lookup"}}, billing,
	}), counter))
	request := httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", strings.NewReader(`{
		"model":"allowed",
		"instructions":"be concise",
		"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}],
		"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],
		"tool_choice":{"type":"function","name":"lookup"},
		"text":{"verbosity":"high"}
	}`))
	request.Header.Set("Authorization", "Bearer test-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"object":"response.input_tokens"`) || !strings.Contains(response.Body.String(), `"input_tokens":23`) {
		t.Fatalf("unexpected response: status=%d body=%s", response.Code, response.Body.String())
	}
	if billing.calls != 0 {
		t.Fatalf("token count opened billing lifecycle: calls=%d", billing.calls)
	}
	if counter.calls != 1 || counter.request.APIKey != "" || counter.request.ResponseRequest == nil || len(counter.request.ResponseRequest.Tools) != 1 {
		t.Fatalf("unsafe or incomplete provider context: calls=%d request=%+v", counter.calls, counter.request)
	}
	text, _ := counter.request.ResponseRequest.Text.(map[string]any)
	if text["verbosity"] != "high" {
		t.Fatalf("text verbosity was lost: %+v", counter.request.ResponseRequest.Text)
	}
}

func TestResponseInputTokenCountRejectsInvalidTextVerbosity(t *testing.T) {
	counter := &responseInputTokenCountProvider{}
	handler := Routes(NewHandler(modules.NewPipeline(nil), counter))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", strings.NewReader(`{"model":"m","input":"x","text":{"verbosity":1}}`)))
	if response.Code != http.StatusBadRequest || counter.calls != 0 || !strings.Contains(response.Body.String(), "text.verbosity") {
		t.Fatalf("invalid verbosity was accepted: status=%d calls=%d body=%s", response.Code, counter.calls, response.Body.String())
	}
}

func TestResponseInputTokenCountRejectsInvalidEnvelope(t *testing.T) {
	for _, body := range []string{`{"input":"hello"}`, `{"model":"m"}`, `{"model":"m","input":[]}`, `{"model":"m","input":42}`} {
		counter := &responseInputTokenCountProvider{}
		handler := Routes(NewHandler(modules.NewPipeline(nil), counter))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest || counter.calls != 0 || !strings.Contains(response.Body.String(), "invalid_request") {
			t.Fatalf("body=%s status=%d calls=%d response=%s", body, response.Code, counter.calls, response.Body.String())
		}
	}
}

func TestResponseInputTokenCountRejectsOptionsInvalidatedByPipeline(t *testing.T) {
	counter := &responseInputTokenCountProvider{}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{rewriteContextModule{rewrite: func(req *modules.RequestContext) {
		req.ResponseRequest.Text = map[string]any{"verbosity": "invalid"}
	}}}), counter))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", strings.NewReader(`{"model":"m","input":"x"}`)))
	if response.Code != http.StatusBadGateway || counter.calls != 0 || !strings.Contains(response.Body.String(), `"module_failed"`) || !strings.Contains(response.Body.String(), "text.verbosity") {
		t.Fatalf("invalid pipeline output continued: status=%d calls=%d body=%s", response.Code, counter.calls, response.Body.String())
	}
}

func TestResponseInputTokenCountEnforcesModelAndToolACL(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{"model", `{"model":"denied","input":"hello"}`},
		{"tool", `{"model":"allowed","input":"hello","tools":[{"type":"function","name":"denied","parameters":{"type":"object"}}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			counter := &responseInputTokenCountProvider{}
			handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"allowed"}, tools: []string{"lookup"}}}), counter))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", strings.NewReader(test.body)))
			if response.Code != http.StatusForbidden || counter.calls != 0 {
				t.Fatalf("policy bypassed: status=%d calls=%d body=%s", response.Code, counter.calls, response.Body.String())
			}
		})
	}
}

func TestResponseInputTokenCountRejectsUnresolvedBuiltInToolResources(t *testing.T) {
	counter := &responseInputTokenCountProvider{}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}, tools: []string{"file_search"}}}), counter))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", strings.NewReader(`{"model":"m","input":"x","tools":[{"type":"file_search","vector_store_ids":["vs_missing"]}]}`)))
	if response.Code != http.StatusServiceUnavailable || counter.calls != 0 || !strings.Contains(response.Body.String(), "vector_store_unavailable") {
		t.Fatalf("unresolved resource reached provider: status=%d calls=%d body=%s", response.Code, counter.calls, response.Body.String())
	}
}

func TestResponseInputTokenCountRequiresOwnedComputerScreenshot(t *testing.T) {
	counter := &responseInputTokenCountProvider{}
	files := &memoryFileStore{files: map[string]filestate.File{"file_screen": {ID: "file_screen", OwnerKey: "foreign"}}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{allowedModels: []string{"*"}, allowedTools: []string{"computer"}}}), counter).
		WithFileStore(files, FileRuntimeConfig{MaxBytes: 1 << 20, OwnerQuotaBytes: 1 << 20}))
	response := httptest.NewRecorder()
	body := `{"model":"m","input":[{"type":"computer_call_output","call_id":"call_1","output":{"type":"computer_screenshot","file_id":"file_screen"}}]}`
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", strings.NewReader(body)))
	if response.Code != http.StatusBadRequest || counter.calls != 0 || !strings.Contains(response.Body.String(), "computer screenshot file is unavailable") {
		t.Fatalf("foreign screenshot reached token counter: status=%d calls=%d body=%s", response.Code, counter.calls, response.Body.String())
	}
}

func TestResponseInputTokenCountUsesOwnedContainerBinding(t *testing.T) {
	counter := &responseInputTokenCountProvider{}
	identity := modules.RequestContext{CredentialID: "credential-1", UserID: "user-1"}
	owner := fileOwnerKey(identity)
	binding := provider.ContainerBinding{Endpoint: "bound-endpoint", Model: "m", Deployment: "deployment-v1"}
	record := containerstate.Record{OwnerKey: owner, Binding: binding, Container: openai.Container{ID: "cntr_owned"}}
	containers := &memoryContainerStore{records: map[string]containerstate.Record{containerKey(owner, "cntr_owned"): record}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}, tools: []string{"code_interpreter"}}}), counter).WithContainerStore(containers))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", strings.NewReader(`{"model":"m","input":"x","tools":[{"type":"code_interpreter","container":"cntr_owned"}]}`)))
	if response.Code != http.StatusOK || counter.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, counter.calls, response.Body.String())
	}
	if counter.request.Metadata[modules.MetadataResponseContainerEndpoint] != binding.Endpoint || counter.request.Metadata[modules.MetadataResponseContainerDeployment] != binding.Deployment || counter.request.Metadata[modules.MetadataResponseContainerModel] != binding.Model {
		t.Fatalf("container binding was not propagated: %+v", counter.request.Metadata)
	}
}

func TestResponseInputTokenCountUsesOwnedShellContainerBinding(t *testing.T) {
	counter := &responseInputTokenCountProvider{}
	identity := modules.RequestContext{CredentialID: "credential-1", UserID: "user-1"}
	owner := fileOwnerKey(identity)
	binding := provider.ContainerBinding{Endpoint: "bound-endpoint", Model: "m", Deployment: "deployment-v1"}
	record := containerstate.Record{OwnerKey: owner, Binding: binding, Container: openai.Container{ID: "cntr_owned"}}
	containers := &memoryContainerStore{records: map[string]containerstate.Record{containerKey(owner, "cntr_owned"): record}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}, tools: []string{"shell"}}}), counter).WithContainerStore(containers))
	response := httptest.NewRecorder()
	body := `{"model":"m","input":"x","tools":[{"type":"shell","environment":{"type":"container_reference","container_id":"cntr_owned"}}]}`
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", strings.NewReader(body)))
	if response.Code != http.StatusOK || counter.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, counter.calls, response.Body.String())
	}
	if counter.request.Metadata[modules.MetadataResponseContainerEndpoint] != binding.Endpoint || counter.request.Metadata[modules.MetadataResponseContainerDeployment] != binding.Deployment || counter.request.Metadata[modules.MetadataResponseContainerModel] != binding.Model {
		t.Fatalf("shell container binding was not propagated: %+v", counter.request.Metadata)
	}
}

func TestResponseInputTokenCountRejectsGenerationOnlyFields(t *testing.T) {
	counter := &responseInputTokenCountProvider{}
	handler := Routes(NewHandler(modules.NewPipeline(nil), counter))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", strings.NewReader(`{"model":"m","input":"x","max_output_tokens":1}`)))
	if response.Code != http.StatusBadRequest || counter.calls != 0 || !strings.Contains(response.Body.String(), "unknown field") {
		t.Fatalf("generation field was accepted: status=%d calls=%d body=%s", response.Code, counter.calls, response.Body.String())
	}
}

func TestResponseInputTokenCountRequiresProviderSupport(t *testing.T) {
	handler := Routes(NewHandler(modules.NewPipeline(nil), &chatProvider{}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", strings.NewReader(`{"model":"m","input":"x"}`)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "unsupported_operation") {
		t.Fatalf("unexpected unsupported response: status=%d body=%s", response.Code, response.Body.String())
	}
}
