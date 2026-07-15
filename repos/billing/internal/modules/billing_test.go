package modules

import (
	"context"
	"math"
	"testing"
	"time"

	"ai-gateway-billing/internal/openai"
)

func TestBillingCollectsChatCompletionEvent(t *testing.T) {
	module := NewBillingModuleWithPricing(true, PricingConfig{
		InputPricePer1K:  0.10,
		OutputPricePer1K: 0.20,
		Currency:         "USD",
	})
	req := RequestContext{
		APIKey: "secret-api-key",
		UserID: "user-1",
		Roles:  []string{"developer"},
		Request: openai.ChatCompletionRequest{
			Provider: "ollama",
			Model:    "test-model",
			Messages: []openai.Message{
				{Role: "user", Content: "hello billing service"},
			},
		},
		Response: &openai.ChatCompletionResponse{
			Model: "test-model",
			Usage: openai.Usage{
				PromptTokens:     12,
				CompletionTokens: 8,
				TotalTokens:      20,
			},
		},
		Metadata: map[string]string{
			"request.id":             "req-1",
			"provider.endpoint.name": "ollama-local",
			"provider.endpoint.type": "ollama",
			"provider.latency_ms":    "123",
			"provider.status":        "ok",
		},
	}

	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}

	event := req.BillingEvent
	if event == nil {
		t.Fatal("expected billing event")
	}
	if event.UserID != "user-1" {
		t.Fatalf("unexpected user id: %s", event.UserID)
	}
	if event.ProviderEndpointName != "ollama-local" {
		t.Fatalf("unexpected endpoint: %s", event.ProviderEndpointName)
	}
	if event.InputTokens != 12 || event.OutputTokens != 8 || event.TotalTokens != 20 {
		t.Fatalf("unexpected tokens: %+v", event)
	}
	if math.Abs(event.Cost-0.0028) > 0.0000001 {
		t.Fatalf("unexpected cost: %.4f", event.Cost)
	}
	if event.APIKeyFingerprint == "" || event.APIKeyFingerprint == "secret-api-key" {
		t.Fatalf("unexpected api key fingerprint: %s", event.APIKeyFingerprint)
	}
	if _, err := time.Parse(time.RFC3339, event.Timestamp); err != nil {
		t.Fatalf("expected RFC 3339 timestamp, got %q: %v", event.Timestamp, err)
	}
}

func TestBillingCollectsResponsesEvent(t *testing.T) {
	module := NewBillingModuleWithPricing(true, PricingConfig{
		InputPricePer1K:  0.01,
		OutputPricePer1K: 0.03,
		Currency:         "USD",
	})
	req := RequestContext{
		UserID: "user-2",
		ResponseRequest: &openai.ResponseRequest{
			Provider:     "openrouter",
			Model:        "gpt-test",
			Input:        "hello responses billing",
			Instructions: "be short",
		},
		ResponsesResponse: &openai.ResponseResponse{
			Model: "gpt-test",
			Usage: openai.ResponseUsage{
				InputTokens:  10,
				OutputTokens: 5,
				TotalTokens:  15,
			},
		},
		Metadata: map[string]string{
			"provider.endpoint.name": "openrouter-key-a",
			"provider.endpoint.type": "openai-compatible",
		},
	}

	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}

	event := req.BillingEvent
	if event == nil {
		t.Fatal("expected billing event")
	}
	if event.APIType != "responses" {
		t.Fatalf("unexpected api type: %s", event.APIType)
	}
	if event.Provider != "openrouter" {
		t.Fatalf("unexpected provider: %s", event.Provider)
	}
	if event.InputTokens != 10 || event.OutputTokens != 5 || event.TotalTokens != 15 {
		t.Fatalf("unexpected tokens: %+v", event)
	}
	if event.ProviderEndpointType != "openai-compatible" {
		t.Fatalf("unexpected endpoint type: %s", event.ProviderEndpointType)
	}
}

func TestBillingEstimatesMultipartMessageContent(t *testing.T) {
	module := NewBillingModuleWithPricing(true, PricingConfig{})
	req := RequestContext{
		Request: openai.ChatCompletionRequest{
			Model: "gpt-5.3",
			Messages: []openai.Message{
				{
					Role: "user",
					Content: []any{
						map[string]any{"type": "text", "text": "hello billing"},
						map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,abc"}},
					},
				},
			},
		},
	}

	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if req.Usage == nil || req.Usage.PromptTokens != 2 {
		t.Fatalf("expected two prompt tokens from text part, got %+v", req.Usage)
	}
}
