package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestOllamaResponsesRejectUnsupportedFunctionToolControls(t *testing.T) {
	strict := true
	strictFalse := false
	var serverCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		serverCalls.Add(1)
		_, _ = w.Write([]byte(ollamaResponseTestJSON))
	}))
	t.Cleanup(server.Close)
	client := NewOllama(server.URL, true)
	for _, test := range []struct {
		name  string
		tool  openai.ResponseTool
		param string
	}{
		{name: "strict", tool: openai.ResponseTool{Type: "function", Name: "lookup", Strict: &strict}, param: "tools.strict"},
		{name: "strict false", tool: openai.ResponseTool{Type: "function", Name: "lookup", Strict: &strictFalse}, param: "tools.strict"},
		{name: "unsupported schema", tool: openai.ResponseTool{Type: "function", Name: "lookup", Parameters: map[string]any{"type": "object", "additionalProperties": false}}, param: "tools.parameters"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := openai.ResponseRequest{Model: "m", Input: "hello", Tools: []openai.ResponseTool{test.tool}}
			for _, stream := range []bool{false, true} {
				before := serverCalls.Load()
				var err error
				if stream {
					_, err = client.StreamResponses(context.Background(), request, nil)
				} else {
					_, err = client.Responses(context.Background(), request)
				}
				var failure *Error
				if !errors.As(err, &failure) || failure.UpstreamCode != "unsupported_parameter" || failure.Param != test.param || serverCalls.Load() != before {
					t.Fatalf("stream=%t calls=%d err=%v, want unsupported %s before HTTP", stream, serverCalls.Load()-before, err, test.param)
				}
			}
		})
	}
}

func TestOllamaResponsesAcceptSupportedFunctionToolSchema(t *testing.T) {
	tool := openai.ResponseTool{Type: "function", Name: "lookup", Parameters: map[string]any{
		"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}},
	}}
	if err := (Ollama{}).ValidateResponseParameters(openai.ResponseRequest{Tools: []openai.ResponseTool{tool}}); err != nil {
		t.Fatalf("supported function tool schema rejected: %v", err)
	}
}
