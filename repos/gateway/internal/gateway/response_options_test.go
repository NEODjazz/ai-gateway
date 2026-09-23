package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/containerstate"
	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
	"ai-gateway-gateway/internal/vectorstate"
)

func TestResponsesRejectsInvalidEnvelopeBeforePipeline(t *testing.T) {
	handler := Handler{}
	for _, body := range []string{
		`{"input":"hello"}`,
		`{"model":"m"}`,
		`{"model":"m","input":null}`,
		`{"model":"m","input":[]}`,
		`{"model":"m","input":42}`,
		`{"model":"m","input":["hello"]}`,
	} {
		out := httptest.NewRecorder()
		handler.Responses(out, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
		if out.Code != 400 || !strings.Contains(out.Body.String(), `"invalid_request"`) {
			t.Fatalf("body=%s status=%d response=%s", body, out.Code, out.Body.String())
		}
	}
}

func TestResponsesReuseOnlyOwnedBoundContainer(t *testing.T) {
	identity := modules.RequestContext{CredentialID: "credential-1", UserID: "user-1"}
	owner := fileOwnerKey(identity)
	binding := provider.ContainerBinding{Endpoint: "bound-endpoint", Model: "m", Deployment: "deployment-v1"}

	for _, test := range []struct {
		name        string
		recordOwner string
		wantStatus  int
	}{
		{name: "owned", recordOwner: owner, wantStatus: 200},
		{name: "foreign", recordOwner: "foreign", wantStatus: 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := &chatProvider{}
			record := containerstate.Record{OwnerKey: test.recordOwner, Binding: binding, Container: openai.Container{ID: "cntr_owned"}}
			containers := &memoryContainerStore{records: map[string]containerstate.Record{containerKey(test.recordOwner, "cntr_owned"): record}}
			handler := NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}, tools: []string{"code_interpreter"}}}), upstream).WithContainerStore(containers)
			out := httptest.NewRecorder()
			handler.Responses(out, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","input":"continue","tools":[{"type":"code_interpreter","container":"cntr_owned"}]}`)))
			if out.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
			}
			if test.wantStatus == 200 {
				if upstream.request.Metadata[modules.MetadataResponseContainerEndpoint] != binding.Endpoint || upstream.request.Metadata[modules.MetadataResponseContainerDeployment] != binding.Deployment || upstream.request.Metadata[modules.MetadataResponseContainerModel] != binding.Model {
					t.Fatalf("container binding was not propagated: %+v", upstream.request.Metadata)
				}
			} else if upstream.request.ResponseRequest != nil {
				t.Fatal("foreign container reached provider")
			}
		})
	}
}

func TestResponsesReuseOnlyOwnedShellContainer(t *testing.T) {
	identity := modules.RequestContext{CredentialID: "credential-1", UserID: "user-1"}
	owner := fileOwnerKey(identity)
	binding := provider.ContainerBinding{Endpoint: "bound-endpoint", Model: "m", Deployment: "deployment-v1"}
	for _, test := range []struct {
		name        string
		recordOwner string
		wantStatus  int
	}{
		{name: "owned", recordOwner: owner, wantStatus: http.StatusOK},
		{name: "foreign", recordOwner: "foreign", wantStatus: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := &chatProvider{}
			record := containerstate.Record{OwnerKey: test.recordOwner, Binding: binding, Container: openai.Container{ID: "cntr_owned"}}
			containers := &memoryContainerStore{records: map[string]containerstate.Record{containerKey(test.recordOwner, "cntr_owned"): record}}
			handler := NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}, tools: []string{"shell"}}}), upstream).WithContainerStore(containers)
			body := `{"model":"m","input":"continue","tools":[{"type":"shell","environment":{"type":"container_reference","container_id":"cntr_owned"}}]}`
			out := httptest.NewRecorder()
			handler.Responses(out, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
			if out.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
			}
			if test.wantStatus == http.StatusOK {
				if upstream.request.Metadata[modules.MetadataResponseContainerEndpoint] != binding.Endpoint || upstream.request.Metadata[modules.MetadataResponseContainerDeployment] != binding.Deployment || upstream.request.Metadata[modules.MetadataResponseContainerModel] != binding.Model {
					t.Fatalf("shell container binding was not propagated: %+v", upstream.request.Metadata)
				}
			} else if upstream.request.ResponseRequest != nil {
				t.Fatal("foreign shell container reached provider")
			}
		})
	}
}

func TestResponsesRejectsEnvelopeInvalidatedByPipeline(t *testing.T) {
	handler := NewHandler(modules.NewPipeline([]modules.Module{rewriteContextModule{rewrite: func(req *modules.RequestContext) {
		req.ResponseRequest.Model = ""
	}}}), nil)
	out := httptest.NewRecorder()
	handler.Responses(out, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","input":"hello"}`)))
	if out.Code != 502 || !strings.Contains(out.Body.String(), `"module_failed"`) {
		t.Fatalf("status=%d response=%s", out.Code, out.Body.String())
	}
}

func TestResponsesRejectsOptionsInvalidatedByPipeline(t *testing.T) {
	handler := NewHandler(modules.NewPipeline([]modules.Module{rewriteContextModule{rewrite: func(req *modules.RequestContext) {
		limit := 0
		req.ResponseRequest.MaxOutputTokens = &limit
	}}}), nil)
	out := httptest.NewRecorder()
	handler.Responses(out, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","input":"hello"}`)))
	if out.Code != 502 || !strings.Contains(out.Body.String(), `"module_failed"`) || !strings.Contains(out.Body.String(), "max_output_tokens") {
		t.Fatalf("status=%d response=%s", out.Code, out.Body.String())
	}
}

func TestResponsesRejectsInvalidOptionsBeforeExecution(t *testing.T) {
	// No pipeline/router: an invalid request must stop before either is invoked.
	handler := Handler{}
	for _, option := range []string{`"max_output_tokens":1,"max_tokens":1000`, `"max_output_tokens":0`, `"max_output_tokens":-1`, `"max_tokens":0`, `"max_tokens":-1`, `"top_logprobs":-1`, `"top_logprobs":21`, `"truncation":""`, `"truncation":"unknown"`, `"temperature":-0.1`, `"temperature":2.1`, `"top_p":-0.1`, `"top_p":1.1`} {
		for _, stream := range []string{"false", "true"} {
			out := httptest.NewRecorder()
			request := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","input":"hello","stream":`+stream+`,`+option+`}`))
			handler.Responses(out, request)
			if out.Code != 400 || !strings.Contains(out.Body.String(), `"invalid_request"`) {
				t.Fatalf("option=%s stream=%s status=%d", option, stream, out.Code)
			}
		}
	}
}

func TestResponsesPreservesServerSideCompactionConfiguration(t *testing.T) {
	upstream := &chatProvider{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}}}), upstream)
	out := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"hello","context_management":[{"type":"compaction","compact_threshold":1000}]}`))
	handler.Responses(out, request)
	if out.Code != http.StatusOK || upstream.request.ResponseRequest == nil {
		t.Fatalf("status=%d request=%+v body=%s", out.Code, upstream.request.ResponseRequest, out.Body.String())
	}
	entries := upstream.request.ResponseRequest.ContextManagement
	if len(entries) != 1 || entries[0].Type != "compaction" || entries[0].CompactThreshold == nil || *entries[0].CompactThreshold != 1000 {
		t.Fatalf("context management was not preserved: %+v", entries)
	}
}

func TestResponsesRejectsInvalidServerSideCompactionBeforeExecution(t *testing.T) {
	for _, value := range []string{
		`[]`,
		`[{"type":"unknown"}]`,
		`[{"type":"compaction","compact_threshold":0}]`,
		`[{"type":"compaction"},{"type":"compaction"}]`,
	} {
		out := httptest.NewRecorder()
		Handler{}.Responses(out, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"hello","context_management":`+value+`}`)))
		if out.Code != http.StatusBadRequest {
			t.Fatalf("value=%s status=%d body=%s", value, out.Code, out.Body.String())
		}
	}
}

func TestResponsesPreservesProviderModerationConfiguration(t *testing.T) {
	upstream := &chatProvider{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}}}), upstream)
	out := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"hello","moderation":{"model":"omni-moderation-latest","policy":{"input":{"mode":"block"},"output":{"mode":"score"}}}}`))
	handler.Responses(out, request)
	if out.Code != http.StatusOK || upstream.request.ResponseRequest == nil || upstream.request.ResponseRequest.Moderation == nil {
		t.Fatalf("status=%d request=%+v body=%s", out.Code, upstream.request.ResponseRequest, out.Body.String())
	}
	moderation := upstream.request.ResponseRequest.Moderation
	if moderation.Model != "omni-moderation-latest" || moderation.Policy == nil || moderation.Policy.Input == nil || moderation.Policy.Input.Mode != "block" || moderation.Policy.Output == nil || moderation.Policy.Output.Mode != "score" {
		t.Fatalf("moderation was not preserved: %+v", moderation)
	}
}

func TestResponsesRejectsInvalidProviderModerationBeforeExecution(t *testing.T) {
	for _, value := range []string{
		`{}`,
		`{"model":"moderation","policy":{"input":{"mode":"allow"}}}`,
		`{"model":"moderation","policy":{"output":{}}}`,
	} {
		out := httptest.NewRecorder()
		Handler{}.Responses(out, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"hello","moderation":`+value+`}`)))
		if out.Code != http.StatusBadRequest {
			t.Fatalf("value=%s status=%d body=%s", value, out.Code, out.Body.String())
		}
	}
}

func TestResponsesValidatesStreamOptionsBeforeExecution(t *testing.T) {
	handler := Handler{}
	for _, body := range []string{
		`{"model":"m","input":"hello","stream_options":{"include_obfuscation":false}}`,
		`{"model":"m","input":"hello","stream":true,"stream_options":{"include_usage":true}}`,
	} {
		out := httptest.NewRecorder()
		handler.Responses(out, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
		if out.Code != 400 || !strings.Contains(out.Body.String(), `"invalid_request"`) {
			t.Fatalf("body=%s status=%d response=%s", body, out.Code, out.Body.String())
		}
	}
}

func TestResponsesRejectsUnsupportedIncludeBeforeExecution(t *testing.T) {
	handler := Handler{}
	for _, include := range []string{`["unknown"]`, `["reasoning.encrypted_content","reasoning.encrypted_content"]`} {
		out := httptest.NewRecorder()
		body := `{"model":"m","input":"hello","include":` + include + `}`
		handler.Responses(out, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
		if out.Code != 400 || !strings.Contains(out.Body.String(), `"invalid_request"`) {
			t.Fatalf("include=%s status=%d response=%s", include, out.Code, out.Body.String())
		}
	}
}

func TestResponsesRejectsInvalidToolChoiceBeforeExecution(t *testing.T) {
	handler := Handler{}
	for _, choice := range []string{`"unknown"`, `"required"`, `{"type":"function","name":"missing"}`, `{"type":"function","name":"lookup","extra":true}`} {
		out := httptest.NewRecorder()
		body := `{"model":"m","input":"hello","tools":[{"type":"function","name":"lookup"}],"tool_choice":` + choice + `}`
		if choice == `"required"` {
			body = `{"model":"m","input":"hello","tool_choice":"required"}`
		}
		handler.Responses(out, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
		if out.Code != 400 || !strings.Contains(out.Body.String(), `"invalid_request"`) {
			t.Fatalf("choice=%s status=%d response=%s", choice, out.Code, out.Body.String())
		}
	}
}

func TestResponsesRejectsMixedToolDefinitionsBeforeExecution(t *testing.T) {
	handler := Handler{}
	for _, tools := range []string{
		`[{"type":"function","name":"lookup","server_url":"https://example.test"}]`,
		`[{"type":"mcp","server_label":"documents","server_url":"https://example.test","parameters":{"type":"object"}}]`,
		`[{"type":"function","name":"lookup"},{"type":"function","name":"lookup"}]`,
	} {
		out := httptest.NewRecorder()
		body := `{"model":"m","input":"hello","tools":` + tools + `}`
		handler.Responses(out, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
		if out.Code != 400 || !strings.Contains(out.Body.String(), `"invalid_request"`) {
			t.Fatalf("tools=%s status=%d response=%s", tools, out.Code, out.Body.String())
		}
	}
}

func TestResponsesAuthorizesCodeInterpreterTool(t *testing.T) {
	for _, test := range []struct {
		name        string
		allowed     []string
		wantStatus  int
		wantForward bool
	}{
		{name: "allowed", allowed: []string{"code_interpreter"}, wantStatus: 200, wantForward: true},
		{name: "denied", allowed: []string{"lookup"}, wantStatus: 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := &chatProvider{}
			handler := NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}, tools: test.allowed}}), upstream)
			out := httptest.NewRecorder()
			handler.Responses(out, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","input":"calculate","tools":[{"type":"code_interpreter","container":{"type":"auto","memory_limit":"4g"}}],"tool_choice":{"type":"code_interpreter"}}`)))
			if out.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
			}
			forwarded := upstream.request.ResponseRequest != nil && len(upstream.request.ResponseRequest.Tools) == 1 && upstream.request.ResponseRequest.Tools[0].Type == "code_interpreter"
			if forwarded != test.wantForward {
				t.Fatalf("forwarded=%t request=%+v", forwarded, upstream.request.ResponseRequest)
			}
		})
	}
}

func TestResponsesAuthorizesWebSearchTool(t *testing.T) {
	for _, test := range []struct {
		name        string
		allowed     []string
		wantStatus  int
		wantForward bool
	}{
		{name: "allowed", allowed: []string{"web_search"}, wantStatus: 200, wantForward: true},
		{name: "denied", allowed: []string{"lookup"}, wantStatus: 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := &chatProvider{}
			handler := NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}, tools: test.allowed}}), upstream)
			out := httptest.NewRecorder()
			handler.Responses(out, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","input":"latest news","tools":[{"type":"web_search","filters":{"allowed_domains":["example.com"]},"search_context_size":"high","user_location":{"type":"approximate","country":"RU","timezone":"Europe/Moscow"}}],"tool_choice":{"type":"web_search"}}`)))
			if out.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
			}
			forwarded := upstream.request.ResponseRequest != nil && len(upstream.request.ResponseRequest.Tools) == 1 && upstream.request.ResponseRequest.Tools[0].Type == "web_search"
			if forwarded != test.wantForward {
				t.Fatalf("forwarded=%t request=%+v", forwarded, upstream.request.ResponseRequest)
			}
		})
	}
}

func TestResponsesAuthorizesComputerTool(t *testing.T) {
	for _, test := range []struct {
		name        string
		allowed     []string
		wantStatus  int
		wantForward bool
	}{
		{name: "allowed", allowed: []string{"computer"}, wantStatus: http.StatusOK, wantForward: true},
		{name: "denied", allowed: []string{"lookup"}, wantStatus: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := &chatProvider{}
			handler := NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}, tools: test.allowed}}), upstream)
			out := httptest.NewRecorder()
			handler.Responses(out, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"open settings","tools":[{"type":"computer"}],"tool_choice":{"type":"computer"}}`)))
			if out.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
			}
			forwarded := upstream.request.ResponseRequest != nil && len(upstream.request.ResponseRequest.Tools) == 1 && upstream.request.ResponseRequest.Tools[0].Type == "computer"
			if forwarded != test.wantForward {
				t.Fatalf("forwarded=%t request=%+v", forwarded, upstream.request.ResponseRequest)
			}
		})
	}
}

func TestResponsesAuthorizesComputerContinuationInput(t *testing.T) {
	for _, test := range []struct {
		name       string
		allowed    []string
		wantStatus int
	}{
		{name: "allowed", allowed: []string{"computer"}, wantStatus: http.StatusOK},
		{name: "denied", allowed: []string{"lookup"}, wantStatus: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := &chatProvider{}
			handler := NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}, tools: test.allowed}}), upstream)
			body := `{"model":"m","previous_response_id":"resp_1","input":[{"type":"computer_call_output","call_id":"call_1","output":{"type":"computer_screenshot","image_url":"data:image/png;base64,iVBORw0KGgo="}}]}`
			out := httptest.NewRecorder()
			handler.Responses(out, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
			if out.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
			}
		})
	}
}

func TestResponsesAuthorizesShellToolAndContinuationInput(t *testing.T) {
	for _, test := range []struct {
		name       string
		allowed    []string
		body       string
		wantStatus int
	}{
		{name: "tool allowed", allowed: []string{"shell"}, body: `{"model":"m","input":"list files","tools":[{"type":"shell","environment":{"type":"local"}}],"tool_choice":{"type":"shell"}}`, wantStatus: http.StatusOK},
		{name: "tool denied", allowed: []string{"lookup"}, body: `{"model":"m","input":"list files","tools":[{"type":"shell"}]}`, wantStatus: http.StatusForbidden},
		{name: "continuation allowed", allowed: []string{"shell"}, body: `{"model":"m","previous_response_id":"resp_1","input":[{"type":"shell_call_output","call_id":"call_1","output":[{"stdout":"ok\\n","stderr":"","outcome":{"type":"exit","exit_code":0}}]}]}`, wantStatus: http.StatusOK},
		{name: "continuation denied", allowed: []string{"lookup"}, body: `{"model":"m","previous_response_id":"resp_1","input":[{"type":"shell_call_output","call_id":"call_1","output":[{"stdout":"ok\\n","stderr":"","outcome":{"type":"exit","exit_code":0}}]}]}`, wantStatus: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := &chatProvider{}
			handler := NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}, tools: test.allowed}}), upstream)
			out := httptest.NewRecorder()
			handler.Responses(out, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(test.body)))
			if out.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
			}
		})
	}
}

func TestResponsesAuthorizesApplyPatchToolAndContinuationInput(t *testing.T) {
	for _, test := range []struct {
		name       string
		allowed    []string
		body       string
		wantStatus int
	}{
		{name: "tool allowed", allowed: []string{"apply_patch"}, body: `{"model":"m","input":"update docs","tools":[{"type":"apply_patch"}],"tool_choice":{"type":"apply_patch"}}`, wantStatus: http.StatusOK},
		{name: "tool denied", allowed: []string{"lookup"}, body: `{"model":"m","input":"update docs","tools":[{"type":"apply_patch"}]}`, wantStatus: http.StatusForbidden},
		{name: "continuation allowed", allowed: []string{"apply_patch"}, body: `{"model":"m","previous_response_id":"resp_1","input":[{"type":"apply_patch_call_output","call_id":"call_1","status":"completed","output":"updated"}]}`, wantStatus: http.StatusOK},
		{name: "continuation denied", allowed: []string{"lookup"}, body: `{"model":"m","previous_response_id":"resp_1","input":[{"type":"apply_patch_call_output","call_id":"call_1","status":"failed","output":"conflict"}]}`, wantStatus: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := &chatProvider{}
			handler := NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}, tools: test.allowed}}), upstream)
			out := httptest.NewRecorder()
			handler.Responses(out, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(test.body)))
			if out.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
			}
		})
	}
}

func TestResponsesRequireOwnedShellAutoFiles(t *testing.T) {
	identity := modules.RequestContext{CredentialID: "credential-1", UserID: "user-1"}
	owner := fileOwnerKey(identity)
	for _, test := range []struct {
		name       string
		fileOwner  string
		wantStatus int
	}{
		{name: "owned", fileOwner: owner, wantStatus: http.StatusOK},
		{name: "foreign", fileOwner: "foreign", wantStatus: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := &chatProvider{}
			files := &memoryFileStore{files: map[string]filestate.File{"file_owned": {ID: "file_owned", OwnerKey: test.fileOwner}}}
			handler := NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}, tools: []string{"shell"}}}), upstream).
				WithFileStore(files, FileRuntimeConfig{MaxBytes: 1 << 20, OwnerQuotaBytes: 1 << 20})
			body := `{"model":"m","input":"inspect","tools":[{"type":"shell","environment":{"type":"container_auto","file_ids":["file_owned"]}}]}`
			out := httptest.NewRecorder()
			handler.Responses(out, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
			if out.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
			}
			if (upstream.request.ResponseRequest != nil) != (test.wantStatus == http.StatusOK) {
				t.Fatalf("unexpected provider execution: request=%+v", upstream.request.ResponseRequest)
			}
		})
	}
}

func TestResponsesResolveOwnedComputerScreenshotBeforePolicy(t *testing.T) {
	identity := modules.RequestContext{CredentialID: "credential", UserID: "user"}
	owner := fileOwnerKey(identity)
	png := []byte("\x89PNG\r\n\x1a\ncontent")
	for _, test := range []struct {
		name       string
		fileOwner  string
		wantStatus int
		wantPolicy int
	}{
		{name: "owned", fileOwner: owner, wantStatus: http.StatusOK, wantPolicy: 1},
		{name: "foreign", fileOwner: "foreign", wantStatus: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := &chatProvider{}
			policy := &lifecycleBillingModule{}
			files := &memoryFileStore{files: map[string]filestate.File{"file_screen": {
				ID: "file_screen", OwnerKey: test.fileOwner, Filename: "screen.png", Purpose: "vision", ContentType: "image/png", Bytes: int64(len(png)), Content: png,
			}}}
			handler := NewHandler(modules.NewPipeline([]modules.Module{
				&lifecycleAuthModule{allowedModels: []string{"*"}, allowedTools: []string{"computer"}}, policy,
			}), upstream).WithFileStore(files, FileRuntimeConfig{MaxBytes: 1 << 20, OwnerQuotaBytes: 1 << 20})
			body := `{"model":"m","previous_response_id":"resp_1","tools":[{"type":"computer"}],"input":[{"type":"computer_call_output","call_id":"call_1","output":{"type":"computer_screenshot","file_id":"file_screen","detail":"original"}}]}`
			out := httptest.NewRecorder()
			handler.Responses(out, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
			if out.Code != test.wantStatus || policy.calls != test.wantPolicy {
				t.Fatalf("status=%d policy=%d body=%s", out.Code, policy.calls, out.Body.String())
			}
			if test.wantStatus != http.StatusOK {
				if upstream.request.ResponseRequest != nil {
					t.Fatal("foreign screenshot reached provider")
				}
				return
			}
			encoded, err := json.Marshal(upstream.request.ResponseRequest.Input)
			if err != nil || strings.Contains(string(encoded), "file_screen") || !strings.Contains(string(encoded), "data:image/png;base64,") {
				t.Fatalf("screenshot was not resolved: input=%s err=%v", encoded, err)
			}
			attachments, err := openai.ResponseImageAttachments(upstream.request.ResponseRequest.Input)
			if err != nil || len(attachments) != 1 {
				t.Fatalf("resolved screenshot did not reach image policy path: attachments=%+v err=%v", attachments, err)
			}
		})
	}
}

func TestResponsesRequireOwnedBuiltInToolResources(t *testing.T) {
	identity := modules.RequestContext{CredentialID: "credential-1", UserID: "user-1"}
	owner := fileOwnerKey(identity)
	body := `{"model":"m","input":"analyze","tools":[{"type":"code_interpreter","container":{"type":"auto","file_ids":["file_owned"]}},{"type":"file_search","vector_store_ids":["vs_owned"]}]}`

	for _, test := range []struct {
		name       string
		fileOwner  string
		storeOwner string
		wantStatus int
	}{
		{name: "owned", fileOwner: owner, storeOwner: owner, wantStatus: 200},
		{name: "foreign file", fileOwner: "foreign", storeOwner: owner, wantStatus: 400},
		{name: "foreign vector store", fileOwner: owner, storeOwner: "foreign", wantStatus: 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := &chatProvider{}
			files := &memoryFileStore{files: map[string]filestate.File{"file_owned": {ID: "file_owned", OwnerKey: test.fileOwner}}}
			vectors := &memoryVectorStore{stores: map[string]vectorstate.VectorStore{"vs_owned": {ID: "vs_owned", OwnerKey: test.storeOwner}}, files: map[string]vectorstate.File{}}
			handler := NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}, tools: []string{"code_interpreter", "file_search"}}}), upstream).
				WithFileStore(files, FileRuntimeConfig{MaxBytes: 1 << 20, OwnerQuotaBytes: 1 << 20}).
				WithVectorStore(vectors, VectorStoreRuntimeConfig{OwnerQuota: 10})
			out := httptest.NewRecorder()
			handler.Responses(out, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
			if out.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
			}
			if (upstream.request.ResponseRequest != nil) != (test.wantStatus == 200) {
				t.Fatalf("unexpected provider execution: request=%+v", upstream.request.ResponseRequest)
			}
		})
	}
}

func TestResponsesRequireOwnedImageGenerationMask(t *testing.T) {
	identity := modules.RequestContext{CredentialID: "credential-1", UserID: "user-1"}
	owner := fileOwnerKey(identity)
	for _, test := range []struct {
		name       string
		fileOwner  string
		wantStatus int
	}{
		{name: "owned", fileOwner: owner, wantStatus: http.StatusOK},
		{name: "foreign", fileOwner: "foreign", wantStatus: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := &chatProvider{}
			files := &memoryFileStore{files: map[string]filestate.File{"file_mask": {ID: "file_mask", OwnerKey: test.fileOwner}}}
			handler := NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{models: []string{"*"}, tools: []string{"image_generation"}}}), upstream).
				WithFileStore(files, FileRuntimeConfig{MaxBytes: 1 << 20, OwnerQuotaBytes: 1 << 20})
			out := httptest.NewRecorder()
			handler.Responses(out, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m","input":"edit","tools":[{"type":"image_generation","action":"edit","input_image_mask":{"file_id":"file_mask"}}]}`)))
			if out.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
			}
			if (upstream.request.ResponseRequest != nil) != (test.wantStatus == http.StatusOK) {
				t.Fatalf("unexpected provider execution: request=%+v", upstream.request.ResponseRequest)
			}
		})
	}
}

func TestResponsesRejectsInvalidReasoningBeforeExecution(t *testing.T) {
	handler := Handler{}
	for _, reasoning := range []string{
		`{"effort":"extreme"}`,
		`{"summary":"full"}`,
		`{"generate_summary":"full"}`,
		`{"context":"previous_turn"}`,
		`{"mode":""}`,
	} {
		out := httptest.NewRecorder()
		body := `{"model":"m","input":"hello","reasoning":` + reasoning + `}`
		handler.Responses(out, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
		if out.Code != 400 || !strings.Contains(out.Body.String(), `"invalid_request"`) {
			t.Fatalf("reasoning=%s status=%d response=%s", reasoning, out.Code, out.Body.String())
		}
	}
}

func TestResponsesRejectsInvalidTextConfigurationBeforeExecution(t *testing.T) {
	handler := Handler{}
	for _, textConfig := range []string{
		`"plain"`,
		`[]`,
		`{"unknown":true}`,
		`{"format":"json_object"}`,
	} {
		out := httptest.NewRecorder()
		body := `{"model":"m","input":"hello","text":` + textConfig + `}`
		handler.Responses(out, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
		if out.Code != 400 || !strings.Contains(out.Body.String(), `"invalid_request"`) {
			t.Fatalf("text=%s status=%d response=%s", textConfig, out.Code, out.Body.String())
		}
	}
}
