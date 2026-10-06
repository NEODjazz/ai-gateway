package modules

import (
	"context"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
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

func TestBillingPreservesExplicitZeroMessagesOutputReserve(t *testing.T) {
	zero := 0
	req := RequestContext{Metadata: map[string]string{"gateway.api_type": "messages"}, Request: openai.ChatCompletionRequest{AllowZeroMaxTokens: true, MaxTokens: &zero, Messages: []openai.Message{{Role: "user", Content: "cache this"}}}}
	reserved := billingRequest(&req)
	if reserved.APIType != "messages" || reserved.OutputTokens != 0 || reserved.TotalTokens != reserved.InputTokens || reserved.InputTokens != openai.ChatInputTokens(req.Request) {
		t.Fatalf("explicit zero output reserve changed: %+v", reserved)
	}
}

func TestBillingReservesOnlyInputForResponsePrewarm(t *testing.T) {
	enabled := true
	limit := 100_000
	response := openai.ResponseRequest{Input: "prepare prompt", MaxOutputTokens: &limit, PromptCacheOptions: &openai.PromptCacheOptions{Prewarm: &enabled}}
	req := RequestContext{ResponseRequest: &response}
	reserved := billingRequest(&req)
	if reserved.APIType != "responses" || reserved.OutputTokens != 0 || reserved.TotalTokens != reserved.InputTokens || reserved.InputTokens != openai.ResponseInputTokens(response) {
		t.Fatalf("prewarm output reserve changed: %+v", reserved)
	}
}

func TestBillingReservesOnlyCachedContentInput(t *testing.T) {
	req := RequestContext{
		Metadata: map[string]string{"gateway.api_type": "cached_content"},
		Request: openai.ChatCompletionRequest{
			Model:    "gemini",
			Messages: []openai.Message{{Role: "user", Content: "cache this"}},
			Tools: []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{
				Name: "lookup", Parameters: map[string]any{"type": "object", "description": strings.Repeat("schema", 100)},
			}}},
		},
	}
	reserved := billingRequest(&req)
	if reserved.APIType != "cached_content" || reserved.InputTokens != openai.ChatInputTokens(req.Request) || reserved.InputTokens < 100 || reserved.OutputTokens != 0 || reserved.TotalTokens != reserved.InputTokens {
		t.Fatalf("cached content reserve=%+v", reserved)
	}
}

func TestLocalBillingUsesImageGenerationUsage(t *testing.T) {
	response := openai.ImageGenerationResponse{Usage: &openai.ImageUsage{InputTokens: 3, OutputTokens: 9, TotalTokens: 12}}
	req := RequestContext{ImageGenerationResponse: &response}
	if err := NewBillingModule(true).Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if req.Usage == nil || req.Usage.PromptTokens != 3 || req.Usage.CompletionTokens != 9 || req.Usage.TotalTokens != 12 {
		t.Fatalf("usage=%+v", req.Usage)
	}
}

func TestLocalBillingUsesAudioTranscriptionUsage(t *testing.T) {
	request := openai.AudioTranscriptionRequest{Model: "audio", Prompt: "names", File: openai.AudioAttachment{Filename: "sample.wav", MediaType: "audio/wav", Data: "UklGRi4uLi5XQVZFZGF0YQ=="}}
	response := openai.AudioTranscriptionResponse{Text: "hello", Usage: &openai.AudioTranscriptionUsage{Type: "tokens", InputTokens: 5, OutputTokens: 2, TotalTokens: 7}}
	req := RequestContext{AudioTranscriptionRequest: &request, AudioTranscriptionResponse: &response}
	if err := NewBillingModule(true).Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if req.Usage == nil || req.Usage.PromptTokens != 5 || req.Usage.CompletionTokens != 2 || req.Usage.TotalTokens != 7 {
		t.Fatalf("usage=%+v", req.Usage)
	}
}

func TestBillingUsesDurationTranscriptionWithoutEstimatedTokens(t *testing.T) {
	request := openai.AudioTranscriptionRequest{Model: "audio", File: openai.AudioAttachment{Filename: "sample.wav", MediaType: "audio/wav", Data: "UklGRi4uLi5XQVZFZGF0YQ=="}}
	response := openai.AudioTranscriptionResponse{Text: "hello", Duration: 1.25, Usage: &openai.AudioTranscriptionUsage{Type: "duration", InputAudioMilliseconds: 10000}}
	req := RequestContext{AudioTranscriptionRequest: &request, AudioTranscriptionResponse: &response, InputAudioMilliseconds: 10000}
	if err := NewBillingModule(true).Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if req.Usage == nil || req.Usage.PromptTokens != 0 || req.Usage.CompletionTokens != 0 || req.Usage.TotalTokens != 0 {
		t.Fatalf("local usage=%+v", req.Usage)
	}
	remote := billingRequest(&req)
	if remote.InputTokens != 0 || remote.OutputTokens != 0 || remote.TotalTokens != 0 || remote.InputAudioMilliseconds != 10000 || remote.UsageEstimated {
		t.Fatalf("remote usage=%+v", remote)
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
