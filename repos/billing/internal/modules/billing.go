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
	catalog                    ModelCatalog
}

type PricingConfig struct {
	InputPricePer1K  float64
	OutputPricePer1K float64
	Currency         string
}

const maxBillableSearchRequests = 1_000_000
const maxBillableInputCharacters = 100_000_000
const maxBillableInputPages = 1_000_000
const maxBillableInputAudioMilliseconds = 7 * 24 * 60 * 60 * 1000
const maxBillableVideoSeconds = 24 * 60 * 60
const maxBillableToolRequests = 1_000_000
const maxBillableTrainingTokens = 1_000_000_000

func NewBillingModule(required bool) BillingModule {
	settings := SettingsFromEnv()
	return NewBillingModuleWithSettings(required, settings)
}

func NewBillingModuleWithPricing(required bool, pricing PricingConfig) BillingModule {
	catalog, _ := ParseModelCatalog("")
	return BillingModule{
		required:  required,
		pricing:   pricing,
		writer:    NoopUsageEventWriter{},
		policy:    NoopPolicyChecker{},
		lifecycle: NewLifecycleStore(),
		catalog:   catalog,
	}
}

func NewBillingModuleWithSettings(required bool, settings Settings) BillingModule {
	catalog, catalogErr := ParseModelCatalog(settings.ModelCatalogJSON)
	module := BillingModule{
		required:                   required,
		pricing:                    settings.Pricing,
		writer:                     NoopUsageEventWriter{},
		policy:                     NewPolicyChecker(settings),
		lifecycle:                  NewLifecycleStore(),
		defaultReserveOutputTokens: settings.DefaultReserveOutputTokens,
		catalog:                    catalog,
		initErr:                    catalogErr,
	}
	if required && settings.UsageEventsEnabled && !settings.DurableOutboxEnabled {
		module.initErr = errors.Join(module.initErr, errors.New("required usage reporting requires BILLING_DURABLE_OUTBOX_ENABLED and POSTGRES_DSN"))
	}
	if !settings.DurableOutboxEnabled && module.initErr == nil {
		module.writer = NewUsageEventWriter(settings)
	}
	if settings.DurableOutboxEnabled {
		writer := UsageEventWriter(NoopUsageEventWriter{})
		if settings.UsageEventsEnabled {
			writer = NewClickHouseUsageEventWriter(settings)
		}
		var outboxErr error
		module.durable, outboxErr = NewPostgresOutboxRepository(settings.PostgresDSN, writer, settings.OutboxPollInterval)
		module.initErr = errors.Join(module.initErr, outboxErr)
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
	if outbox, ok := m.writer.(*AsyncUsageOutbox); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = outbox.Close(ctx)
	}

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
	if req.SearchRequests < 0 || req.SearchRequests > maxBillableSearchRequests {
		return errors.New("search_requests is outside the supported range")
	}
	if req.InputCharacters < 0 || req.InputCharacters > maxBillableInputCharacters {
		return errors.New("input_characters is outside the supported range")
	}
	if req.InputPages < 0 || req.InputPages > maxBillableInputPages {
		return errors.New("input_pages is outside the supported range")
	}
	if req.InputAudioMilliseconds < 0 || req.InputAudioMilliseconds > maxBillableInputAudioMilliseconds {
		return errors.New("input_audio_milliseconds is outside the supported range")
	}
	if req.VideoSeconds < 0 || req.VideoSeconds > maxBillableVideoSeconds {
		return errors.New("video_seconds is outside the supported range")
	}
	if req.ToolRequests < 0 || req.ToolRequests > maxBillableToolRequests {
		return errors.New("tool_requests is outside the supported range")
	}
	if req.TrainingTokens < 0 || req.TrainingTokens > maxBillableTrainingTokens {
		return errors.New("training_tokens is outside the supported range")
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
	inputTokens, outputTokens, totalTokens, usageEstimated := usageTokens(req, promptTokens)
	if req.APIType == "video" {
		inputTokens, outputTokens, totalTokens = 0, 0, 0
		usageEstimated = metadataBool(req, "usage.estimated")
	}
	inputCharacters := req.InputCharacters
	inputPages := req.InputPages
	inputAudioMilliseconds := req.InputAudioMilliseconds
	videoSeconds := req.VideoSeconds
	trainingTokens := req.TrainingTokens
	if metadata(req, "provider.cache.status") == "hit" {
		inputCharacters = 0
		inputPages = 0
		inputAudioMilliseconds = 0
		videoSeconds = 0
		trainingTokens = 0
	}
	if phase == "cancel" {
		usageEstimated = false
	}

	req.Usage = &openai.Usage{
		SearchRequests:   req.SearchRequests,
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
	req.Metadata["billing.usage_estimated"] = strconv.FormatBool(usageEstimated)
	req.Metadata["billing.search_requests"] = strconv.Itoa(req.SearchRequests)
	req.Metadata["billing.input_characters"] = strconv.Itoa(inputCharacters)
	req.Metadata["billing.input_pages"] = strconv.Itoa(inputPages)
	req.Metadata["billing.input_audio_milliseconds"] = strconv.Itoa(inputAudioMilliseconds)
	req.Metadata["billing.video_seconds"] = strconv.Itoa(videoSeconds)
	req.Metadata["billing.tool_requests"] = strconv.Itoa(req.ToolRequests)
	req.Metadata["billing.training_tokens"] = strconv.Itoa(trainingTokens)
	req.Metadata["billing.search_requests_estimated"] = strconv.FormatBool(req.SearchRequestsEstimated)

	eventOutputTokens, eventTotalTokens := outputTokens, totalTokens
	if phase == "reserve" && eventOutputTokens == 0 && req.APIType != "video" {
		eventOutputTokens = reserveOutputTokens(req, m.defaultReserveOutputTokens)
		eventTotalTokens = inputTokens + eventOutputTokens
		req.Metadata["billing.reserved_output_tokens"] = strconv.Itoa(eventOutputTokens)
	}
	if eventTotalTokens > int(^uint(0)>>1)-trainingTokens {
		return errors.New("total token accounting overflow")
	}
	eventTotalTokens += trainingTokens
	event, err := m.event(req, promptTokens, inputTokens, eventOutputTokens, eventTotalTokens, trainingTokens, inputCharacters, inputPages, inputAudioMilliseconds, videoSeconds, usageEstimated)
	pricingErr := err
	if pricingErr != nil && phase == "reserve" {
		return err
	}
	req.BillingEvent = &event
	event.Phase = phase
	key := ""
	if event.RequestID != "" {
		key = event.RequestID + ":" + phase
	}
	event.EventID = key
	if policy, ok := m.policy.(*PostgresBudgetPolicyChecker); ok {
		if repository, ok := m.durable.(*PostgresOutboxRepository); ok {
			return m.handleDurableBudget(ctx, req, &event, policy, repository, pricingErr)
		}
	}
	if err := m.policy.Apply(ctx, &event); err != nil {
		return err
	}
	if pricingErr != nil && phase == "commit" && event.CatalogVersion == "" {
		return pricingErr
	}
	if m.durable != nil {
		if phase == "reserve" {
			created, err := m.durable.Reserve(ctx, event)
			if err != nil {
				cancelEvent := event
				cancelEvent.Phase = "cancel"
				cancelEvent.InputTokens, cancelEvent.OutputTokens, cancelEvent.TotalTokens, cancelEvent.TrainingTokens, cancelEvent.InputCharacters, cancelEvent.InputPages, cancelEvent.InputAudioMilliseconds, cancelEvent.VideoSeconds, cancelEvent.ToolRequests, cancelEvent.SearchRequests, cancelEvent.Cost = 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0
				_ = m.policy.Apply(ctx, &cancelEvent)
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
			event.InputTokens, event.OutputTokens, event.TotalTokens, event.TrainingTokens, event.InputCharacters, event.InputPages, event.InputAudioMilliseconds, event.VideoSeconds, event.ToolRequests, event.SearchRequests, event.Cost = 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0
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
	created, claimErr := m.lifecycle.Claim(key)
	if claimErr != nil {
		return claimErr
	}
	if !created {
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
		event.TrainingTokens = 0
		event.InputCharacters = 0
		event.InputPages = 0
		event.InputAudioMilliseconds = 0
		event.VideoSeconds = 0
		event.ToolRequests = 0
		event.Cost = 0
	}
	if err := m.writer.WriteUsageEvent(ctx, event); err != nil {
		m.lifecycle.Release(key)
		return err
	}
	return nil
}

func (m BillingModule) event(req *RequestContext, promptTokens int, inputTokens int, outputTokens int, totalTokens int, trainingTokens int, inputCharacters int, inputPages int, inputAudioMilliseconds int, videoSeconds int, usageEstimated bool) (BillingEvent, error) {
	model := req.Request.Model
	providerName := req.Request.Provider
	apiType := req.APIType
	if apiType == "" {
		apiType = "chat_completions"
	}
	if req.ResponseRequest != nil {
		model = req.ResponseRequest.Model
		providerName = req.ResponseRequest.Provider
		if req.APIType == "" {
			apiType = "responses"
		}
	}
	pricing, supplied, suppliedErr := suppliedPricingSnapshot(req)
	if apiType == "mcp_tools_list" || apiType == "mcp_tools_call" {
		pricing = PricingSnapshot{Currency: m.pricing.Currency}
		supplied = true
		suppliedErr = nil
	}
	var err error
	if !supplied && suppliedErr == nil {
		pricing, err = m.catalog.Resolve(metadata(req, "provider.endpoint.name"), metadata(req, "provider.endpoint.type"), providerName, model, m.pricing)
	} else {
		err = suppliedErr
	}
	pricingErr := err
	if pricingErr != nil {
		pricing = PricingSnapshot{Currency: m.pricing.Currency}
	}
	// Raw upstream error strings may contain request fragments or provider
	// internals. Persist only the bounded failure class used by operators.
	return BillingEvent{
		RequestID:               requestID(req),
		SessionID:               req.SessionID,
		TraceID:                 req.TraceID,
		UserID:                  req.UserID,
		TeamID:                  req.TeamID,
		OrganizationID:          req.OrganizationID,
		Roles:                   append([]string(nil), req.Roles...),
		Tags:                    append([]string(nil), req.Tags...),
		APIKeyFingerprint:       req.CredentialID,
		Provider:                providerName,
		ProviderID:              metadata(req, "provider.id"),
		ProviderEndpointName:    metadata(req, "provider.endpoint.name"),
		ProviderEndpointType:    metadata(req, "provider.endpoint.type"),
		Model:                   model,
		UpstreamModel:           upstreamModel(req),
		APIType:                 apiType,
		Phase:                   req.BillingPhase,
		Status:                  metadataDefault(req, "provider.status", "ok"),
		FailureClass:            metadata(req, "provider.failure_class"),
		LatencyMS:               metadataInt(req, "provider.latency_ms"),
		FirstTokenLatencyMS:     metadataInt(req, "provider.first_token_latency_ms"),
		RetryCount:              metadataInt(req, "provider.retry_count"),
		FallbackCount:           metadataInt(req, "provider.fallback_count"),
		CacheStatus:             metadata(req, "provider.cache.status"),
		CacheKind:               metadata(req, "provider.cache.kind"),
		UsageEstimated:          usageEstimated,
		PromptTokensEstimated:   promptTokens,
		InputCharacters:         inputCharacters,
		InputPages:              inputPages,
		InputAudioMilliseconds:  inputAudioMilliseconds,
		VideoSeconds:            videoSeconds,
		ToolRequests:            req.ToolRequests,
		InputTokens:             inputTokens,
		OutputTokens:            outputTokens,
		TotalTokens:             totalTokens,
		TrainingTokens:          trainingTokens,
		CacheReadInputTokens:    cacheReadInputTokens(req),
		CacheWriteInputTokens:   cacheWriteInputTokens(req),
		SearchRequests:          req.SearchRequests,
		SearchRequestsEstimated: req.SearchRequestsEstimated,
		Cost:                    pricingCost(inputTokens, outputTokens, trainingTokens, inputCharacters, inputPages, inputAudioMilliseconds, videoSeconds, req.SearchRequests, pricing),
		Currency:                pricing.Currency,
		CatalogVersion:          pricing.CatalogVersion,
		PricingKey:              pricing.PricingKey,
		InputCostPer1M:          pricing.InputCostPer1M,
		OutputCostPer1M:         pricing.OutputCostPer1M,
		TrainingCostPer1M:       pricing.TrainingCostPer1M,
		SearchCostPer1K:         pricing.SearchCostPer1K,
		CharacterCostPer1M:      pricing.CharacterCostPer1M,
		PageCostPer1K:           pricing.PageCostPer1K,
		AudioCostPerMinute:      pricing.AudioCostPerMinute,
		VideoCostPerSecond:      pricing.VideoCostPerSecond,
		Timestamp:               time.Now().UTC().Format(time.RFC3339),
	}, pricingErr
}

func cacheReadInputTokens(req *RequestContext) int {
	value := req.CacheReadInputTokens
	if value == 0 && req.Response != nil && req.Response.Usage.PromptTokensDetails != nil {
		value = req.Response.Usage.PromptTokensDetails.CachedTokens
	}
	if value == 0 && req.ResponsesResponse != nil && req.ResponsesResponse.Usage.InputTokensDetails != nil {
		value = req.ResponsesResponse.Usage.InputTokensDetails.CachedTokens
	}
	return max(value, 0)
}

func cacheWriteInputTokens(req *RequestContext) int {
	value := req.CacheWriteInputTokens
	if value == 0 && req.Response != nil && req.Response.Usage.PromptTokensDetails != nil {
		value = firstNonZero(req.Response.Usage.PromptTokensDetails.CacheWriteTokens, req.Response.Usage.PromptTokensDetails.CacheCreationTokens)
	}
	if value == 0 && req.ResponsesResponse != nil && req.ResponsesResponse.Usage.InputTokensDetails != nil {
		value = firstNonZero(req.ResponsesResponse.Usage.InputTokensDetails.CacheWriteTokens, req.ResponsesResponse.Usage.InputTokensDetails.CacheCreationTokens)
	}
	return max(value, 0)
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func upstreamModel(req *RequestContext) string {
	if model := metadata(req, "provider.upstream_model"); model != "" {
		return model
	}
	if req.Response != nil {
		return req.Response.Model
	}
	if req.ResponsesResponse != nil {
		return req.ResponsesResponse.Model
	}
	return ""
}

func suppliedPricingSnapshot(req *RequestContext) (PricingSnapshot, bool, error) {
	version := metadata(req, "model_catalog.version")
	if version == "" {
		return PricingSnapshot{}, false, nil
	}
	pricingKey := metadata(req, "model_catalog.pricing_key")
	currency := metadata(req, "model_catalog.currency")
	input, inputErr := strconv.ParseFloat(metadata(req, "model_catalog.input_cost_per_1m"), 64)
	output, outputErr := strconv.ParseFloat(metadata(req, "model_catalog.output_cost_per_1m"), 64)
	training := 0.0
	trainingErr := error(nil)
	if raw := metadata(req, "model_catalog.training_cost_per_1m"); raw != "" {
		training, trainingErr = strconv.ParseFloat(raw, 64)
	}
	search := 0.0
	searchErr := error(nil)
	if raw := metadata(req, "model_catalog.search_cost_per_1k"); raw != "" {
		search, searchErr = strconv.ParseFloat(raw, 64)
	}
	characters := 0.0
	charactersErr := error(nil)
	if raw := metadata(req, "model_catalog.character_cost_per_1m"); raw != "" {
		characters, charactersErr = strconv.ParseFloat(raw, 64)
	}
	pages := 0.0
	pagesErr := error(nil)
	if raw := metadata(req, "model_catalog.page_cost_per_1k"); raw != "" {
		pages, pagesErr = strconv.ParseFloat(raw, 64)
	}
	audio := 0.0
	audioErr := error(nil)
	if raw := metadata(req, "model_catalog.audio_cost_per_minute"); raw != "" {
		audio, audioErr = strconv.ParseFloat(raw, 64)
	}
	video := 0.0
	videoErr := error(nil)
	if raw := metadata(req, "model_catalog.video_cost_per_second"); raw != "" {
		video, videoErr = strconv.ParseFloat(raw, 64)
	}
	if pricingKey == "" || len(currency) != 3 || inputErr != nil || outputErr != nil || trainingErr != nil || searchErr != nil || charactersErr != nil || pagesErr != nil || audioErr != nil || videoErr != nil || input < 0 || output < 0 || training < 0 || search < 0 || characters < 0 || pages < 0 || audio < 0 || video < 0 {
		return PricingSnapshot{}, true, errors.New("invalid supplied pricing snapshot")
	}
	return PricingSnapshot{CatalogVersion: version, PricingKey: pricingKey, Currency: currency, InputCostPer1M: input, OutputCostPer1M: output, TrainingCostPer1M: training, SearchCostPer1K: search, CharacterCostPer1M: characters, PageCostPer1K: pages, AudioCostPerMinute: audio, VideoCostPerSecond: video}, true, nil
}

func requestID(req *RequestContext) string {
	if req.RequestID != "" {
		return req.RequestID
	}
	return metadata(req, "request.id")
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

func usageTokens(req *RequestContext, fallbackPromptTokens int) (int, int, int, bool) {
	if metadata(req, "provider.cache.status") == "hit" {
		return 0, 0, 0, false
	}
	if req.Usage != nil && (req.Usage.TotalTokens > 0 || req.APIType == "realtime" && !metadataBool(req, "usage.estimated")) {
		return req.Usage.PromptTokens, req.Usage.CompletionTokens, req.Usage.TotalTokens, metadataBool(req, "usage.estimated")
	}
	if req.Response != nil && req.Response.Usage.TotalTokens > 0 {
		return req.Response.Usage.PromptTokens, req.Response.Usage.CompletionTokens, req.Response.Usage.TotalTokens, false
	}
	if req.ResponsesResponse != nil && req.ResponsesResponse.Usage.TotalTokens > 0 {
		return req.ResponsesResponse.Usage.InputTokens, req.ResponsesResponse.Usage.OutputTokens, req.ResponsesResponse.Usage.TotalTokens, false
	}
	return fallbackPromptTokens, 0, fallbackPromptTokens, true
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

func metadataBool(req *RequestContext, key string) bool {
	value, _ := strconv.ParseBool(metadata(req, key))
	return value
}

// Budget state and delivery intent commit together. A database failure cannot
// leave a finalized budget entry without its durable delivery event.
func (m BillingModule) handleDurableBudget(ctx context.Context, req *RequestContext, event *BillingEvent, policy *PostgresBudgetPolicyChecker, repository *PostgresOutboxRepository, pricingErr error) error {
	if policy.initErr != nil {
		return policy.initErr
	}
	if event.RequestID == "" || (event.Phase != "reserve" && event.Phase != "commit" && event.Phase != "cancel") {
		return errors.New("invalid billing lifecycle")
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := policy.applyTx(ctx, tx, event); err != nil {
		return err
	}
	if pricingErr != nil && event.Phase == "commit" && event.CatalogVersion == "" {
		return pricingErr
	}
	created := false
	if event.Phase == "reserve" {
		tag, err := tx.Exec(ctx, `INSERT INTO billing_event_ledger(event_id,request_id,phase) VALUES($1,$2,$3) ON CONFLICT(event_id) DO NOTHING`, event.EventID, event.RequestID, event.Phase)
		if err != nil {
			return err
		}
		created = tag.RowsAffected() == 1
	} else {
		if event.Phase == "cancel" {
			event.InputTokens, event.OutputTokens, event.TotalTokens, event.TrainingTokens, event.InputCharacters, event.InputPages, event.InputAudioMilliseconds, event.VideoSeconds, event.ToolRequests, event.SearchRequests, event.Cost = 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0
		}
		created, err = repository.enqueueTx(ctx, tx, *event)
		if err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if !created {
		req.Metadata["billing.idempotent_replay"] = "true"
	}
	return nil
}
