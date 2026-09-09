package modules

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"ai-gateway-billing/internal/openai"
)

type recordingUsageWriter struct {
	events []BillingEvent
	err    error
}

type recordingPolicyChecker struct {
	events []BillingEvent
}

func (c *recordingPolicyChecker) Apply(_ context.Context, event *BillingEvent) error {
	c.events = append(c.events, *event)
	return nil
}
func (*recordingPolicyChecker) Ready(context.Context) error { return nil }
func (*recordingPolicyChecker) Close()                      {}

type fakeDurableRepository struct {
	seen   map[string]bool
	events []BillingEvent
}

func (r *fakeDurableRepository) Ready(context.Context) error { return nil }

func (r *fakeDurableRepository) Reserve(_ context.Context, event BillingEvent) (bool, error) {
	if r.seen[event.EventID] {
		return false, nil
	}
	r.seen[event.EventID] = true
	return true, nil
}

func (r *fakeDurableRepository) Enqueue(_ context.Context, event BillingEvent) (bool, error) {
	if r.seen[event.EventID] {
		return false, nil
	}
	r.seen[event.EventID] = true
	r.events = append(r.events, event)
	return true, nil
}

func (w *recordingUsageWriter) WriteUsageEvent(_ context.Context, event BillingEvent) error {
	if w.err != nil {
		return w.err
	}
	w.events = append(w.events, event)
	return nil
}

func TestBillingCollectsChatCompletionEvent(t *testing.T) {
	module := NewBillingModuleWithPricing(true, PricingConfig{
		InputPricePer1K:  0.10,
		OutputPricePer1K: 0.20,
		Currency:         "USD",
	})
	req := RequestContext{
		CredentialID:   "safe-fingerprint",
		TraceID:        "0123456789abcdef0123456789abcdef",
		UserID:         "user-1",
		TeamID:         "team-1",
		OrganizationID: "org-1",
		Roles:          []string{"developer"},
		Tags:           []string{"production", "cost-center-a"},
		Request: openai.ChatCompletionRequest{
			Provider: "ollama",
			Model:    "test-model",
			Messages: []openai.Message{
				{Role: "user", Content: "hello billing service"},
			},
		},
		Response: &openai.ChatCompletionResponse{
			Model: "test-model-2026-08-01",
			Usage: openai.Usage{
				PromptTokens:     12,
				CompletionTokens: 8,
				TotalTokens:      20,
				PromptTokensDetails: &openai.PromptTokenDetails{
					CachedTokens:        7,
					CacheCreationTokens: 3,
				},
			},
		},
		Metadata: map[string]string{
			"provider.id":                     "ollama",
			"request.id":                      "req-1",
			"provider.endpoint.name":          "ollama-local",
			"provider.endpoint.type":          "ollama",
			"provider.latency_ms":             "123",
			"provider.first_token_latency_ms": "45",
			"provider.retry_count":            "2",
			"provider.fallback_count":         "1",
			"provider.cache.status":           "miss",
			"provider.status":                 "ok",
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
	if event.ProviderID != "ollama" || event.Model != "test-model" || event.UpstreamModel != "test-model-2026-08-01" {
		t.Fatalf("unexpected canonical usage identity: %+v", event)
	}
	if event.InputTokens != 12 || event.OutputTokens != 8 || event.TotalTokens != 20 || event.CacheReadInputTokens != 7 || event.CacheWriteInputTokens != 3 {
		t.Fatalf("unexpected tokens: %+v", event)
	}
	if math.Abs(event.Cost-0.0028) > 0.0000001 {
		t.Fatalf("unexpected cost: %.4f", event.Cost)
	}
	if event.APIKeyFingerprint != "safe-fingerprint" {
		t.Fatalf("unexpected api key fingerprint: %s", event.APIKeyFingerprint)
	}
	if event.OrganizationID != "org-1" || len(event.Tags) != 2 || event.Tags[0] != "production" || event.TraceID != "0123456789abcdef0123456789abcdef" || event.FirstTokenLatencyMS != 45 || event.RetryCount != 2 || event.FallbackCount != 1 || event.UsageEstimated {
		t.Fatalf("unexpected usage observability: %+v", event)
	}
	if _, err := time.Parse(time.RFC3339, event.Timestamp); err != nil {
		t.Fatalf("expected RFC 3339 timestamp, got %q: %v", event.Timestamp, err)
	}
}

func TestBillingPersistsBoundedFailureClassWithoutRawProviderError(t *testing.T) {
	module := NewBillingModuleWithPricing(true, PricingConfig{Currency: "USD"})
	req := RequestContext{
		BillingPhase: "cancel",
		Request:      openai.ChatCompletionRequest{Provider: "openai", Model: "test-model"},
		Metadata: map[string]string{
			"provider.status":        "error",
			"provider.failure_class": "upstream",
			"provider.error":         "secret request fragment must not be retained",
		},
	}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if req.BillingEvent == nil || req.BillingEvent.FailureClass != "upstream" || req.BillingEvent.Error != "" {
		t.Fatalf("unsafe failure event: %+v", req.BillingEvent)
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

func TestBillingPreservesExplicitEmbeddingsAPIType(t *testing.T) {
	module := NewBillingModuleWithPricing(true, PricingConfig{})
	req := RequestContext{
		APIType: "embeddings", BillingPhase: "commit", PromptTokensEstimated: 3,
		Request: openai.ChatCompletionRequest{Provider: "ollama", Model: "nomic-embed"},
		Usage:   &openai.Usage{PromptTokens: 3, TotalTokens: 3},
	}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if req.BillingEvent == nil || req.BillingEvent.APIType != "embeddings" {
		t.Fatalf("unexpected billing event: %+v", req.BillingEvent)
	}
	if req.BillingEvent.InputTokens != 3 || req.BillingEvent.OutputTokens != 0 || req.BillingEvent.TotalTokens != 3 {
		t.Fatalf("unexpected embedding token accounting: %+v", req.BillingEvent)
	}
}

func TestBillingPreservesNativeChatSurfaceAPIType(t *testing.T) {
	for _, apiType := range []string{"messages", "generate_content"} {
		t.Run(apiType, func(t *testing.T) {
			module := NewBillingModuleWithPricing(true, PricingConfig{})
			req := RequestContext{
				APIType: apiType, BillingPhase: "commit", PromptTokensEstimated: 3,
				Request: openai.ChatCompletionRequest{Provider: "native", Model: "model"},
				Usage:   &openai.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
			}
			if err := module.Handle(context.Background(), &req); err != nil {
				t.Fatal(err)
			}
			if req.BillingEvent == nil || req.BillingEvent.APIType != apiType {
				t.Fatalf("unexpected billing event: %+v", req.BillingEvent)
			}
		})
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
	if req.BillingEvent == nil || !req.BillingEvent.UsageEstimated || req.Metadata["billing.usage_estimated"] != "true" {
		t.Fatalf("estimated usage was not identified: event=%+v metadata=%+v", req.BillingEvent, req.Metadata)
	}
}

func TestBillingPreservesUpstreamEstimatedUsageFlag(t *testing.T) {
	module := NewBillingModuleWithPricing(true, PricingConfig{Currency: "USD"})
	req := RequestContext{
		BillingPhase: "commit",
		Request:      openai.ChatCompletionRequest{Model: "model"},
		Usage:        &openai.Usage{PromptTokens: 4, TotalTokens: 4},
		Metadata:     map[string]string{"usage.estimated": "true"},
	}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if req.BillingEvent == nil || !req.BillingEvent.UsageEstimated {
		t.Fatalf("estimated flag was lost: %+v", req.BillingEvent)
	}
}

func TestBillingLifecycleIsIdempotentPerRequestAndPhase(t *testing.T) {
	writer := &recordingUsageWriter{}
	module := BillingModule{required: true, pricing: PricingConfig{Currency: "USD"}, writer: writer, policy: NoopPolicyChecker{}, lifecycle: NewLifecycleStore()}
	req := RequestContext{
		RequestID: "req-idempotent", BillingPhase: "commit", PostResponse: true,
		Request: openai.ChatCompletionRequest{Model: "model"},
		Usage:   &openai.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
	}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if len(writer.events) != 1 || req.Metadata["billing.idempotent_replay"] != "true" {
		t.Fatalf("expected one committed event, got events=%d metadata=%+v", len(writer.events), req.Metadata)
	}
}

func TestBillingFailedWriteCanBeRetried(t *testing.T) {
	writer := &recordingUsageWriter{err: errors.New("temporary failure")}
	module := BillingModule{required: true, pricing: PricingConfig{}, writer: writer, policy: NoopPolicyChecker{}, lifecycle: NewLifecycleStore()}
	req := RequestContext{RequestID: "req-retry", BillingPhase: "commit", PostResponse: true}
	if err := module.Handle(context.Background(), &req); err == nil {
		t.Fatal("expected first write to fail")
	}
	writer.err = nil
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatalf("released idempotency key should allow retry: %v", err)
	}
	if len(writer.events) != 1 {
		t.Fatalf("expected one successful event, got %d", len(writer.events))
	}
}

func TestBillingCancelHasNoUsageOrCost(t *testing.T) {
	writer := &recordingUsageWriter{}
	module := BillingModule{required: true, pricing: PricingConfig{InputPricePer1K: 1, OutputPricePer1K: 1}, writer: writer, policy: NoopPolicyChecker{}, lifecycle: NewLifecycleStore()}
	req := RequestContext{RequestID: "req-cancel", BillingPhase: "cancel", Usage: &openai.Usage{PromptTokens: 10, TotalTokens: 10}}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if len(writer.events) != 1 || writer.events[0].Phase != "cancel" || writer.events[0].TotalTokens != 0 || writer.events[0].Cost != 0 {
		t.Fatalf("unexpected cancel event: %+v", writer.events)
	}
}

func TestBillingReservesConfiguredOutputAllowanceAndTeamScope(t *testing.T) {
	policy := &recordingPolicyChecker{}
	module := BillingModule{
		required: true, pricing: PricingConfig{Currency: "USD"}, writer: NoopUsageEventWriter{},
		policy: policy, lifecycle: NewLifecycleStore(), defaultReserveOutputTokens: 64,
	}
	req := RequestContext{
		RequestID: "req-reserve", TeamID: "team-42", CredentialID: "credential-42",
		Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "one two"}}},
	}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if len(policy.events) != 1 || policy.events[0].Phase != "reserve" || policy.events[0].TeamID != "team-42" {
		t.Fatalf("unexpected policy event: %+v", policy.events)
	}
	if policy.events[0].InputTokens != 2 || policy.events[0].OutputTokens != 64 || policy.events[0].TotalTokens != 66 {
		t.Fatalf("unexpected reserved usage: %+v", policy.events[0])
	}
	if req.Usage == nil || req.Usage.TotalTokens != 2 {
		t.Fatalf("reservation must not overwrite actual usage: %+v", req.Usage)
	}
}

func TestBillingCacheHitDoesNotChargeProviderTokens(t *testing.T) {
	writer := &recordingUsageWriter{}
	module := BillingModule{required: true, pricing: PricingConfig{InputPricePer1K: 1}, writer: writer, policy: NoopPolicyChecker{}, lifecycle: NewLifecycleStore()}
	req := RequestContext{
		RequestID: "req-cache-hit", BillingPhase: "commit", PostResponse: true,
		Request:         openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user", Content: "cached prompt"}}},
		InputCharacters: 4096,
		InputPages:      4,
		Metadata:        map[string]string{"provider.cache.status": "hit"},
	}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if len(writer.events) != 1 || writer.events[0].CacheStatus != "hit" || writer.events[0].TotalTokens != 0 || writer.events[0].InputCharacters != 0 || writer.events[0].InputPages != 0 || writer.events[0].Cost != 0 {
		t.Fatalf("cache hit must not charge provider usage: %+v", writer.events)
	}
}

func TestBillingUsesDurableRepositoryForIdempotentCommit(t *testing.T) {
	repository := &fakeDurableRepository{seen: map[string]bool{}}
	module := BillingModule{
		required: true, pricing: PricingConfig{}, policy: NoopPolicyChecker{},
		lifecycle: NewLifecycleStore(), durable: repository,
	}
	req := RequestContext{RequestID: "req-durable", BillingPhase: "commit", PostResponse: true}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if len(repository.events) != 1 || repository.events[0].EventID != "req-durable:commit" {
		t.Fatalf("unexpected durable events: %+v", repository.events)
	}
	if req.Metadata["billing.idempotent_replay"] != "true" {
		t.Fatalf("duplicate durable event was not identified: %+v", req.Metadata)
	}
}

func TestDurableBillingFailsClosedWithoutPostgresDSN(t *testing.T) {
	module := NewBillingModuleWithSettings(true, Settings{DurableOutboxEnabled: true})
	if err := module.Ready(context.Background()); err == nil {
		t.Fatal("durable billing must not report ready without PostgreSQL")
	}
	if err := module.Handle(context.Background(), &RequestContext{RequestID: "req"}); err == nil {
		t.Fatal("durable billing must fail closed without PostgreSQL")
	}
}

func TestBillingRejectsInvalidSearchRequestCounts(t *testing.T) {
	module := NewBillingModuleWithPricing(true, PricingConfig{Currency: "USD"})
	for _, count := range []int{-1, maxBillableSearchRequests + 1} {
		req := RequestContext{RequestID: "invalid-search-count", SearchRequests: count}
		if err := module.Handle(context.Background(), &req); err == nil {
			t.Fatalf("accepted search_requests=%d", count)
		}
	}
}

func TestBillingRejectsInvalidInputPageCounts(t *testing.T) {
	module := NewBillingModuleWithPricing(true, PricingConfig{Currency: "USD"})
	for _, count := range []int{-1, maxBillableInputPages + 1} {
		req := RequestContext{RequestID: "invalid-page-count", InputPages: count}
		if err := module.Handle(context.Background(), &req); err == nil {
			t.Fatalf("accepted input_pages=%d", count)
		}
	}
}
