package modules

import (
	"ai-gateway-gateway/internal/openai"
	"strings"
	"testing"
)

func TestBillingReserveIncludesModernCapAndToolSchema(t *testing.T) {
	n := 10000
	req := RequestContext{Request: openai.ChatCompletionRequest{MaxCompletionTokens: &n, Messages: []openai.Message{{Role: "user", Content: "test"}}, Tools: []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "tool", Parameters: map[string]any{"description": strings.Repeat("schema", 1000)}}}}}}
	modern := billingRequest(&req)
	req.Request.MaxCompletionTokens = nil
	req.Request.MaxTokens = &n
	legacy := billingRequest(&req)
	if modern.OutputTokens != 10000 || modern.InputTokens < 1000 || modern.TotalTokens != legacy.TotalTokens || modern.OutputTokens != legacy.OutputTokens {
		t.Fatal("inconsistent reserve")
	}
	if modern.InputTokens != openai.ChatInputTokens(req.Request) {
		t.Fatal("billing/TPM input estimate mismatch")
	}
}
