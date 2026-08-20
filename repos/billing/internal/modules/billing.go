package modules

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"ai-gateway-billing/internal/openai"
)

type BillingModule struct {
	required                   bool
	pricing                    PricingConfig
	writer                     UsageEventWriter
	policy                     PolicyChecker
	lifecycle                  *LifecycleStore
	durable                    DurableEventRepository
	initErr                    error
	defaultReserveOutputTokens int
}

type PricingConfig struct {
	InputPricePer1K  float64
	OutputPricePer1K float64
	Currency         string
}

func NewBillingModule(required bool) BillingModule {
	settings := SettingsFromEnv()
	return NewBillingModuleWithSettings(required, settings)
}

func NewBillingModuleWithPricing(required bool, pricing PricingConfig) BillingModule {
	return BillingModule{
		required:  required,
		pricing:   pricing,
		writer:    NoopUsageEventWriter{},
		policy:    NoopPolicyChecker{},
		lifecycle: NewLifecycleStore(),
	}
}

func NewBillingModuleWithSettings(required bool, settings Settings) BillingModule {
	module := BillingModule{
		required:                   required,
		pricing:                    settings.Pricing,
		writer:                     NewUsageEventWriter(settings),
		policy:                     NewPolicyChecker(settings),
		lifecycle:                  NewLifecycleStore(),
		defaultReserveOutputTokens: settings.DefaultReserveOutputTokens,
	}
	if settings.DurableOutboxEnabled {
		writer := UsageEventWriter(NoopUsageEventWriter{})
		if settings.UsageEventsEnabled {
			writer = NewClickHouseUsageEventWriter(settings)
		}
		module.durable, module.initErr = NewPostgresOutboxRepository(settings.PostgresDSN, writer, settings.OutboxPollInterval)
	}
	return module
}

func PricingConfigFromEnv() PricingConfig {
	return PricingConfig{
		InputPricePer1K:  envFloat("BILLING_INPUT_PRICE_PER_1K", 0),
		OutputPricePer1K: envFloat("BILLING_OUTPUT_PRICE_PER_1K", 0),
		Currency:         env("BILLING_CURRENCY", "USD"),
	}
}

func (m BillingModule) Name() string {
	return "billing"
}

func (m BillingModule) Required() bool {
	return m.required
}

func (m BillingModule) Ready(ctx context.Context) error {
	if m.initErr != nil {
		return m.initErr
	}
	if m.durable != nil {
		if err := m.durable.Ready(ctx); err != nil {
			return err
		}
	}
	return m.policy.Ready(ctx)
}

func (m BillingModule) Close() {
	m.policy.Close()
	if repository, ok := m.durable.(*PostgresOutboxRepository); ok {
		repository.Close()
	}
}

func (m BillingModule) PostResponseEnabled() bool {
	return true
}

func (m BillingModule) HandlePostResponse(ctx context.Context, req *RequestContext) error {
	return m.Handle(ctx, req)
}

func (m BillingModule) Handle(ctx context.Context, req *RequestContext) error {
	if m.initErr != nil {
		return m.initErr
	}
	phase := req.BillingPhase
	if phase == "" {
		if req.PostResponse || req.Response != nil || req.ResponsesResponse != nil {
			phase = "commit"
		} else {
			phase = "reserve"
		}
	}
	promptTokens := estimatePromptTokens(req)
	inputTokens, outputTokens, totalTokens := usageTokens(req, promptTokens)

	req.Usage = &openai.Usage{
		PromptTokens:     inputTokens,
		CompletionTokens: outputTokens,
		TotalTokens:      totalTokens,
	}

	if req.Metadata == nil {
		req.Metadata = map[string]string{}
	}
	req.Metadata["billing.prompt_tokens_estimated"] = strconv.Itoa(promptTokens)
	req.Metadata["billing.input_tokens"] = strconv.Itoa(inputTokens)
	req.Metadata["billing.output_tokens"] = strconv.Itoa(outputTokens)
	req.Metadata["billing.total_tokens"] = strconv.Itoa(totalTokens)

	eventOutputTokens, eventTotalTokens := outputTokens, totalTokens
	if phase == "reserve" && eventOutputTokens == 0 {
		eventOutputTokens = reserveOutputTokens(req, m.defaultReserveOutputTokens)
		eventTotalTokens = inputTokens + eventOutputTokens
		req.Metadata["billing.reserved_output_tokens"] = strconv.Itoa(eventOutputTokens)
	}
	event := m.event(req, promptTokens, inputTokens, eventOutputTokens, eventTotalTokens)
	req.BillingEvent = &event
	event.Phase = phase
	key := ""
	if event.RequestID != "" {
		key = event.RequestID + ":" + phase
	}
	event.EventID = key
	if err := m.policy.Apply(ctx, event); err != nil {
		return err
	}
	if m.durable != nil {
		if phase == "reserve" {
			created, err := m.durable.Reserve(ctx, event)
			if err != nil {
				cancelEvent := event
				cancelEvent.Phase = "cancel"
				cancelEvent.InputTokens, cancelEvent.OutputTokens, cancelEvent.TotalTokens, cancelEvent.Cost = 0, 0, 0, 0
				_ = m.policy.Apply(ctx, cancelEvent)
				return err
			}
			if !created {
				req.Metadata["billing.idempotent_replay"] = "true"
			}
			return nil
		}
		if phase != "commit" && phase != "cancel" {
			return errors.New("invalid billing phase: " + phase)
		}
		if phase == "cancel" {
			event.InputTokens, event.OutputTokens, event.TotalTokens, event.Cost = 0, 0, 0, 0
		}
		created, err := m.durable.Enqueue(ctx, event)
		if err != nil {
			return err
		}
		if !created {
			req.Metadata["billing.idempotent_replay"] = "true"
		}
		return nil
	}
	if !m.lifecycle.Begin(key) {
		req.Metadata["billing.idempotent_replay"] = "true"
		return nil
	}
	if phase == "reserve" {
		return nil
	}
	if phase != "commit" && phase != "cancel" {
		m.lifecycle.Release(key)
		return errors.New("invalid billing phase: " + phase)
	}
	if phase == "cancel" {
		event.InputTokens = 0
		event.OutputTokens = 0
		event.TotalTokens = 0
		event.Cost = 0
	}
	if err := m.writer.WriteUsageEvent(ctx, event); err != nil {
		m.lifecycle.Release(key)
		return err
	}
	return nil
}

func (m BillingModule) event(req *RequestContext, promptTokens int, inputTokens int, outputTokens int, totalTokens int) BillingEvent {
	model := req.Request.Model
	providerName := req.Request.Provider
	apiType := "chat_completions"
	if req.ResponseRequest != nil {
		model = req.ResponseRequest.Model
		providerName = req.ResponseRequest.Provider
		apiType = "responses"
	}
	if req.Response != nil && req.Response.Model != "" {
		model = req.Response.Model
	}
	if req.ResponsesResponse != nil && req.ResponsesResponse.Model != "" {
		model = req.ResponsesResponse.Model
	}

	return BillingEvent{
		RequestID:             requestID(req),
		UserID:                req.UserID,
		TeamID:                req.TeamID,
		Roles:                 append([]string(nil), req.Roles...),
		APIKeyFingerprint:     req.CredentialID,
		Provider:              providerName,
		ProviderEndpointName:  metadata(req, "provider.endpoint.name"),
		ProviderEndpointType:  metadata(req, "provider.endpoint.type"),
		Model:                 model,
		APIType:               apiType,
		Phase:                 req.BillingPhase,
		Status:                metadataDefault(req, "provider.status", "ok"),
		Error:                 metadata(req, "provider.error"),
		LatencyMS:             metadataInt(req, "provider.latency_ms"),
		CacheStatus:           metadata(req, "provider.cache.status"),
		PromptTokensEstimated: promptTokens,
		InputTokens:           inputTokens,
		OutputTokens:          outputTokens,
		TotalTokens:           totalTokens,
		Cost:                  m.cost(inputTokens, outputTokens),
		Currency:              m.pricing.Currency,
		Timestamp:             time.Now().UTC().Format(time.RFC3339),
	}
}

func requestID(req *RequestContext) string {
	if req.RequestID != "" {
		return req.RequestID
	}
	return metadata(req, "request.id")
}

func (m BillingModule) cost(inputTokens int, outputTokens int) float64 {
	return (float64(inputTokens)/1000)*m.pricing.InputPricePer1K + (float64(outputTokens)/1000)*m.pricing.OutputPricePer1K
}

func reserveOutputTokens(req *RequestContext, fallback int) int {
	if req.ResponseRequest != nil {
		if req.ResponseRequest.MaxOutputTokens != nil && *req.ResponseRequest.MaxOutputTokens > 0 {
			return *req.ResponseRequest.MaxOutputTokens
		}
	}
	if fallback > 0 {
		return fallback
	}
	return 0
}

func estimatePromptTokens(req *RequestContext) int {
	if req.PromptTokensEstimated > 0 {
		return req.PromptTokensEstimated
	}
	promptTokens := 0
	for _, message := range req.Request.Messages {
		promptTokens += estimateTokens(openai.ContentText(message.Content))
	}
	if req.ResponseRequest != nil {
		promptTokens += estimateTokens(textFromAny(req.ResponseRequest.Input))
		promptTokens += estimateTokens(req.ResponseRequest.Instructions)
	}
	return promptTokens
}

func usageTokens(req *RequestContext, fallbackPromptTokens int) (int, int, int) {
	if metadata(req, "provider.cache.status") == "hit" {
		return 0, 0, 0
	}
	if req.Usage != nil && req.Usage.TotalTokens > 0 {
		return req.Usage.PromptTokens, req.Usage.CompletionTokens, req.Usage.TotalTokens
	}
	if req.Response != nil && req.Response.Usage.TotalTokens > 0 {
		return req.Response.Usage.PromptTokens, req.Response.Usage.CompletionTokens, req.Response.Usage.TotalTokens
	}
	if req.ResponsesResponse != nil && req.ResponsesResponse.Usage.TotalTokens > 0 {
		return req.ResponsesResponse.Usage.InputTokens, req.ResponsesResponse.Usage.OutputTokens, req.ResponsesResponse.Usage.TotalTokens
	}
	return fallbackPromptTokens, 0, fallbackPromptTokens
}

func textFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, textFromAny(item))
		}
		return strings.Join(parts, " ")
	case map[string]any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, textFromAny(item))
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

func estimateTokens(value string) int {
	words := strings.Fields(value)
	if len(words) == 0 {
		return 0
	}
	return len(words)
}

func metadata(req *RequestContext, key string) string {
	if req.Metadata == nil {
		return ""
	}
	return req.Metadata[key]
}

func metadataDefault(req *RequestContext, key string, fallback string) string {
	value := metadata(req, key)
	if value == "" {
		return fallback
	}
	return value
}

func metadataInt(req *RequestContext, key string) int {
	value := metadata(req, key)
	parsed, _ := strconv.Atoi(value)
	return parsed
}
