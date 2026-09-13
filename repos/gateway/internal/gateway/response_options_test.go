package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"
)

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
