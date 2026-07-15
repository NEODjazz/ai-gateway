package modules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"ai-gateway-billing/internal/openai"
)

type BillingModule struct {
	required bool
	pricing  PricingConfig
	writer   UsageEventWriter
	policy   PolicyChecker
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
		required: required,
		pricing:  pricing,
		writer:   NoopUsageEventWriter{},
		policy:   NoopPolicyChecker{},
	}
}

func NewBillingModuleWithSettings(required bool, settings Settings) BillingModule {
	return BillingModule{
		required: required,
		pricing:  settings.Pricing,
		writer:   NewUsageEventWriter(settings),
		policy:   NewPolicyChecker(settings),
	}
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

func (m BillingModule) PostResponseEnabled() bool {
	return true
}

func (m BillingModule) HandlePostResponse(ctx context.Context, req *RequestContext) error {
	return m.Handle(ctx, req)
}

func (m BillingModule) Handle(ctx context.Context, req *RequestContext) error {
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

	event := m.event(req, promptTokens, inputTokens, outputTokens, totalTokens)
	req.BillingEvent = &event
	if err := m.policy.Check(ctx, event); err != nil {
		return err
	}
	if req.Response == nil && req.ResponsesResponse == nil {
		return nil
	}
	if err := m.writer.WriteUsageEvent(ctx, event); err != nil {
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
		RequestID:             metadata(req, "request.id"),
		UserID:                req.UserID,
		Roles:                 append([]string(nil), req.Roles...),
		APIKeyFingerprint:     fingerprint(req.APIKey),
		Provider:              providerName,
		ProviderEndpointName:  metadata(req, "provider.endpoint.name"),
		ProviderEndpointType:  metadata(req, "provider.endpoint.type"),
		Model:                 model,
		APIType:               apiType,
		Status:                metadataDefault(req, "provider.status", "ok"),
		Error:                 metadata(req, "provider.error"),
		LatencyMS:             metadataInt(req, "provider.latency_ms"),
		PromptTokensEstimated: promptTokens,
		InputTokens:           inputTokens,
		OutputTokens:          outputTokens,
		TotalTokens:           totalTokens,
		Cost:                  m.cost(inputTokens, outputTokens),
		Currency:              m.pricing.Currency,
		Timestamp:             time.Now().UTC().Format(time.RFC3339),
	}
}

func (m BillingModule) cost(inputTokens int, outputTokens int) float64 {
	return (float64(inputTokens)/1000)*m.pricing.InputPricePer1K + (float64(outputTokens)/1000)*m.pricing.OutputPricePer1K
}

func estimatePromptTokens(req *RequestContext) int {
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

func fingerprint(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
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
