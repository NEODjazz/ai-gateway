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

func TestBillingReserveIncludesEveryChatChoice(t *testing.T) {
	maxTokens, choices := 200, 3
	req := RequestContext{Request: openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{N: &choices}, MaxCompletionTokens: &maxTokens, Messages: []openai.Message{{Role: "user", Content: "test"}}}}
	reserved := billingRequest(&req)
	if reserved.OutputTokens != 600 || reserved.TotalTokens != reserved.InputTokens+600 {
		t.Fatalf("multi-choice output was not fully reserved: %+v", reserved)
	}
}

func TestChatProviderIdentifiersDoNotReplaceBillingIdentity(t *testing.T) {
	for _, options := range []openai.ChatGenerationOptions{
		{SafetyIdentifier: "provider-user"},
		{User: "legacy-user"},
	} {
		req := RequestContext{UserID: "authenticated-user", Request: openai.ChatCompletionRequest{ChatGenerationOptions: options}}
		if reserved := billingRequest(&req); reserved.UserID != "authenticated-user" {
			t.Fatalf("request identifier replaced billing identity: %+v", reserved)
		}
	}
}

func TestResponseSafetyIdentifierDoesNotReplaceBillingIdentity(t *testing.T) {
	response := openai.ResponseRequest{SafetyIdentifier: "provider-user"}
	req := RequestContext{UserID: "authenticated-user", ResponseRequest: &response}
	if reserved := billingRequest(&req); reserved.UserID != "authenticated-user" {
		t.Fatalf("request identifier replaced billing identity: %+v", reserved)
	}
}

func TestPromptCacheKeyDoesNotReplaceBillingIdentity(t *testing.T) {
	response := openai.ResponseRequest{PromptCacheKey: "provider-cache"}
	for _, req := range []RequestContext{
		{UserID: "authenticated-user", Request: openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{PromptCacheKey: "provider-cache"}}},
		{UserID: "authenticated-user", ResponseRequest: &response},
	} {
		if reserved := billingRequest(&req); reserved.UserID != "authenticated-user" {
			t.Fatalf("prompt cache key replaced billing identity: %+v", reserved)
		}
	}
}
