package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
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
