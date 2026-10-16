package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestOllamaResponsesRejectUndeclaredFunctionCalls(t *testing.T) {
	tests := []struct {
		name       string
		choice     any
		tools      []openai.ResponseTool
		outputName string
		wantError  bool
	}{
		{name: "tool choice none", choice: "none", tools: []openai.ResponseTool{{Type: "function", Name: "lookup"}}, outputName: "lookup", wantError: true},
		{name: "undeclared name", tools: []openai.ResponseTool{{Type: "function", Name: "lookup"}}, outputName: "secret", wantError: true},
		{name: "no tools", outputName: "lookup", wantError: true},
		{name: "declared name", tools: []openai.ResponseTool{{Type: "function", Name: "lookup"}}, outputName: "lookup"},
	}
	for _, test := range tests {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", test.name, stream), func(t *testing.T) {
				item := fmt.Sprintf(`{"type":"function_call","id":"item-1","call_id":"call-1","name":%q,"arguments":"{}"}`, test.outputName)
				response := fmt.Sprintf(`{"id":"r","object":"response","model":"m","status":"completed","output":[%s],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`, item)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if stream {
						_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":%s}\n\n", item)
						_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
						return
					}
					_, _ = fmt.Fprint(w, response)
				}))
				t.Cleanup(server.Close)
				request := openai.ResponseRequest{Model: "m", Input: "hello", Stream: stream, ToolChoice: test.choice, Tools: test.tools}
				client := NewOllama(server.URL, true)
				var payloads []string
				var err error
				if stream {
					_, err = client.StreamResponses(context.Background(), request, func(_ string, payload string) error {
						payloads = append(payloads, payload)
						return nil
					})
				} else {
					_, err = client.Responses(context.Background(), request)
				}
				if test.wantError {
					if err == nil || !strings.Contains(err.Error(), "invalid Ollama response tool call") {
						t.Fatalf("expected undeclared function call rejection, got %v", err)
					}
					if len(payloads) != 0 {
						t.Fatalf("unauthorized function call was streamed: %v", payloads)
					}
				} else if err != nil || stream && len(payloads) != 2 {
					t.Fatalf("declared function call rejected: err=%v payloads=%v", err, payloads)
				}
			})
		}
	}
}

func TestOllamaResponsesRejectFunctionCallChangedByTerminalSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintln(w, `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item-1","call_id":"call-1","name":"lookup","arguments":"{}"}}`)
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, `data: {"type":"response.completed","response":{"id":"r","object":"response","model":"m","status":"completed","output":[{"type":"function_call","id":"item-1","call_id":"call-1","name":"secret","arguments":"{}"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
		_, _ = fmt.Fprintln(w)
	}))
	t.Cleanup(server.Close)
	request := openai.ResponseRequest{Model: "m", Input: "hello", Stream: true, Tools: []openai.ResponseTool{{Type: "function", Name: "lookup"}}}
	var events []string
	_, err := NewOllama(server.URL, true).StreamResponses(context.Background(), request, func(event, _ string) error {
		events = append(events, event)
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "invalid Ollama response tool call") {
		t.Fatalf("expected changed terminal function call rejection, got %v", err)
	}
	if len(events) != 1 || events[0] != "response.output_item.added" {
		t.Fatalf("unexpected events before rejection: %v", events)
	}
}
