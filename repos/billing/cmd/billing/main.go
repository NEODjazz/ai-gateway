package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"ai-gateway-billing/internal/modules"
	"ai-gateway-billing/internal/openai"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	settings := modules.SettingsFromEnv()
	module := modules.NewBillingModuleWithSettings(true, settings)
	defer module.Close()
	auditStore, auditErr := modules.NewPostgresAuditStore(settings.PostgresDSN)
	if auditStore != nil {
		defer auditStore.Close()
	}

	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := module.Ready(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		if auditErr != nil || auditStore == nil || auditStore.Ready(r.Context()) != nil {
			http.Error(w, "audit storage unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		stats := module.UsageDeliveryStats()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(w, "billing_memory_outbox_accepted_total %d\nbilling_memory_outbox_delivered_total %d\nbilling_memory_outbox_failed_total %d\nbilling_memory_outbox_dropped_total %d\nbilling_memory_outbox_rejected_total %d\nbilling_memory_outbox_queued %d\n", stats.Accepted, stats.Delivered, stats.Failed, stats.Dropped, stats.Rejected, stats.Queued)
	})
	http.HandleFunc("/livez", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	manager, managerErr := module.BudgetManager()
	registerBudgetManagement(http.DefaultServeMux, manager, managerErr, os.Getenv("BILLING_MANAGEMENT_SHARED_SECRET"))
	reporter, reporterErr := modules.NewClickHouseUsageReporter(settings)
	registerUsageManagement(http.DefaultServeMux, reporter, reporterErr, os.Getenv("BILLING_MANAGEMENT_SHARED_SECRET"))
	registerRequestLogManagement(http.DefaultServeMux, reporter, reporterErr, os.Getenv("BILLING_MANAGEMENT_SHARED_SECRET"))
	registerAuditManagement(http.DefaultServeMux, auditStore, auditErr, os.Getenv("BILLING_MANAGEMENT_SHARED_SECRET"))

	http.HandleFunc("/usage", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeBillingUsage(w, r, os.Getenv("BILLING_SHARED_SECRET")) {
			return
		}
		var request usageRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx := modules.RequestContext{
			RequestID:               request.RequestID,
			SessionID:               request.SessionID,
			TraceID:                 request.TraceID,
			CredentialID:            request.CredentialID,
			UserID:                  request.UserID,
			TeamID:                  request.TeamID,
			OrganizationID:          request.OrganizationID,
			Roles:                   request.Roles,
			Tags:                    request.Tags,
			PromptTokensEstimated:   request.PromptTokensEstimated,
			InputCharacters:         request.InputCharacters,
			InputPages:              request.InputPages,
			InputAudioMilliseconds:  request.InputAudioMilliseconds,
			VideoSeconds:            request.VideoSeconds,
			ToolRequests:            request.ToolRequests,
			TrainingTokens:          request.TrainingTokens,
			CacheReadInputTokens:    request.CacheReadInputTokens,
			CacheWriteInputTokens:   request.CacheWriteInputTokens,
			SearchRequests:          request.SearchRequests,
			SearchRequestsEstimated: request.SearchRequestsEstimated,
			ProviderCostUSDTicks:    request.ProviderCostUSDTicks,
			PostResponse:            request.Phase == "commit",
			BillingPhase:            request.Phase,
			APIType:                 request.APIType,
			Request: openai.ChatCompletionRequest{
				Provider: request.Provider,
				Model:    request.Model,
			},
			Usage: &openai.Usage{
				SearchRequests:   request.SearchRequests,
				PromptTokens:     request.InputTokens,
				CompletionTokens: request.OutputTokens,
				TotalTokens:      request.TotalTokens,
			},
			Metadata: map[string]string{
				"provider.id":                         request.ProviderID,
				"provider.endpoint.name":              request.ProviderEndpointName,
				"provider.endpoint.type":              request.ProviderEndpointType,
				"provider.status":                     request.Status,
				"provider.error":                      request.Error,
				"provider.failure_class":              request.FailureClass,
				"provider.latency_ms":                 request.LatencyMS,
				"provider.first_token_latency_ms":     request.FirstTokenLatencyMS,
				"provider.retry_count":                strconv.Itoa(request.RetryCount),
				"provider.fallback_count":             strconv.Itoa(request.FallbackCount),
				"provider.cache.status":               request.CacheStatus,
				"provider.cache.kind":                 request.CacheKind,
				"usage.estimated":                     strconv.FormatBool(request.UsageEstimated),
				"model_catalog.version":               request.CatalogVersion,
				"model_catalog.pricing_key":           request.PricingKey,
				"model_catalog.input_cost_per_1m":     request.InputCostPer1M,
				"model_catalog.output_cost_per_1m":    request.OutputCostPer1M,
				"model_catalog.training_cost_per_1m":  request.TrainingCostPer1M,
				"model_catalog.search_cost_per_1k":    request.SearchCostPer1K,
				"model_catalog.character_cost_per_1m": request.CharacterCostPer1M,
				"model_catalog.page_cost_per_1k":      request.PageCostPer1K,
				"model_catalog.audio_cost_per_minute": request.AudioCostPerMinute,
				"model_catalog.video_cost_per_second": request.VideoCostPerSecond,
				"model_catalog.currency":              request.Currency,
				"provider.upstream_model":             request.UpstreamModel,
			},
		}
		if request.APIType == "responses" {
			ctx.ResponseRequest = &openai.ResponseRequest{Provider: request.Provider, Model: request.Model}
		}
		if err := module.Handle(r.Context(), &ctx); err != nil {
			if errors.Is(err, modules.ErrBudgetExceeded) {
				http.Error(w, "budget exceeded", http.StatusTooManyRequests)
			} else if errors.Is(err, modules.ErrBillingConflict) {
				http.Error(w, "billing lifecycle conflict", http.StatusConflict)
			} else {
				http.Error(w, "billing unavailable", http.StatusServiceUnavailable)
			}
			return
		}
		_ = json.NewEncoder(w).Encode(usageResponse{Usage: ctx.Usage, Metadata: billingMetadata(ctx.Metadata)})
	})

	log.Println("billing listening on :8083")
	server := &http.Server{Addr: ":8083", Handler: http.DefaultServeMux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	finished := make(chan error, 1)
	go func() { finished <- server.ListenAndServe() }()
	select {
	case err := <-finished:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("billing server stopped: %w", err)
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			log.Print("billing shutdown deadline exceeded")
		}
		<-finished
	}
	return nil
}

func authorizeBillingUsage(w http.ResponseWriter, r *http.Request, secret string) bool {
	if secret == "" {
		return true
	}
	provided := r.Header.Get("X-Service-Token")
	if len(provided) != len(secret) || subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

type usageRequest struct {
	RequestID               string   `json:"request_id,omitempty"`
	SessionID               string   `json:"session_id,omitempty"`
	TraceID                 string   `json:"trace_id,omitempty"`
	CredentialID            string   `json:"credential_id,omitempty"`
	UserID                  string   `json:"user_id,omitempty"`
	TeamID                  string   `json:"team_id,omitempty"`
	OrganizationID          string   `json:"organization_id,omitempty"`
	Roles                   []string `json:"roles,omitempty"`
	Tags                    []string `json:"tags,omitempty"`
	Provider                string   `json:"provider,omitempty"`
	ProviderID              string   `json:"provider_id,omitempty"`
	ProviderEndpointName    string   `json:"provider_endpoint_name,omitempty"`
	ProviderEndpointType    string   `json:"provider_endpoint_type,omitempty"`
	Model                   string   `json:"model,omitempty"`
	UpstreamModel           string   `json:"upstream_model,omitempty"`
	APIType                 string   `json:"api_type"`
	Phase                   string   `json:"phase"`
	Status                  string   `json:"status,omitempty"`
	Error                   string   `json:"error,omitempty"`
	FailureClass            string   `json:"failure_class,omitempty"`
	LatencyMS               string   `json:"latency_ms,omitempty"`
	FirstTokenLatencyMS     string   `json:"first_token_latency_ms,omitempty"`
	RetryCount              int      `json:"retry_count"`
	FallbackCount           int      `json:"fallback_count"`
	CacheStatus             string   `json:"cache_status,omitempty"`
	CacheKind               string   `json:"cache_kind,omitempty"`
	UsageEstimated          bool     `json:"usage_estimated"`
	PromptTokensEstimated   int      `json:"prompt_tokens_estimated"`
	InputCharacters         int      `json:"input_characters"`
	InputPages              int      `json:"input_pages"`
	InputAudioMilliseconds  int      `json:"input_audio_milliseconds"`
	VideoSeconds            int      `json:"video_seconds"`
	ToolRequests            int      `json:"tool_requests"`
	TrainingTokens          int      `json:"training_tokens"`
	InputTokens             int      `json:"input_tokens"`
	OutputTokens            int      `json:"output_tokens"`
	TotalTokens             int      `json:"total_tokens"`
	CacheReadInputTokens    int      `json:"cache_read_input_tokens"`
	CacheWriteInputTokens   int      `json:"cache_write_input_tokens"`
	SearchRequests          int      `json:"search_requests"`
	SearchRequestsEstimated bool     `json:"search_requests_estimated"`
	ProviderCostUSDTicks    *int64   `json:"provider_cost_usd_ticks,omitempty"`
	CatalogVersion          string   `json:"catalog_version,omitempty"`
	PricingKey              string   `json:"pricing_key,omitempty"`
	InputCostPer1M          string   `json:"input_cost_per_1m,omitempty"`
	OutputCostPer1M         string   `json:"output_cost_per_1m,omitempty"`
	TrainingCostPer1M       string   `json:"training_cost_per_1m,omitempty"`
	SearchCostPer1K         string   `json:"search_cost_per_1k,omitempty"`
	CharacterCostPer1M      string   `json:"character_cost_per_1m,omitempty"`
	PageCostPer1K           string   `json:"page_cost_per_1k,omitempty"`
	AudioCostPerMinute      string   `json:"audio_cost_per_minute,omitempty"`
	VideoCostPerSecond      string   `json:"video_cost_per_second,omitempty"`
	Currency                string   `json:"currency,omitempty"`
}

type usageResponse struct {
	Usage    *openai.Usage     `json:"usage,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

func billingMetadata(metadata map[string]string) map[string]string {
	result := map[string]string{}
	for key, value := range metadata {
		if len(key) >= len("billing.") && key[:len("billing.")] == "billing." {
			result[key] = value
		}
	}
	return result
}
