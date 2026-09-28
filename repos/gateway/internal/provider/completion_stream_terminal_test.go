package provider

import (
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestCompletionStreamRequiresTerminalOutcome(t *testing.T) {
	const partial = `data: {"id":"cmpl","object":"text_completion","created":1,"model":"model","choices":[{"index":0,"text":"partial"}]}` + "\n\n"
	for _, test := range []struct {
		name  string
		body  string
		valid bool
	}{
		{name: "partial", body: partial},
		{name: "partial with usage", body: partial + `data: {"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n\n"},
		{name: "done", body: partial + `data: {"choices":[{"index":0,"finish_reason":"stop"}]}` + "\n\n" + "data: [DONE]\n\n", valid: true},
		{name: "finish reason without done", body: `data: {"id":"cmpl","object":"text_completion","created":1,"model":"model","choices":[{"index":0,"text":"complete","finish_reason":"stop"}]}` + "\n\n", valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := streamCompletionData(strings.NewReader(test.body), openai.CompletionRequest{Model: "model", Prompt: "hello"}, nil)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%t err=%v", test.valid, err)
			}
		})
	}
}
