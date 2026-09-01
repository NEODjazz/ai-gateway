package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"

	"ai-gateway-billing/internal/modules"
	"ai-gateway-billing/internal/openai"
)

func main() {
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
			RequestID:             request.RequestID,
			SessionID:             request.SessionID,
			TraceID:               request.TraceID,
			CredentialID:          request.CredentialID,
			UserID:                request.UserID,
			TeamID:                request.TeamID,
			OrganizationID:        request.OrganizationID,
			Roles:                 request.Roles,
			PromptTokensEstimated: request.PromptTokensEstimated,
			PostResponse:          request.Phase == "commit",
			BillingPhase:          request.Phase,
			APIType:               request.APIType,
			Request: openai.ChatCompletionRequest{
				Provider: request.Provider,
				Model:    request.Model,
			},
			Usage: &openai.Usage{
				PromptTokens:     request.InputTokens,
				CompletionTokens: request.OutputTokens,
				TotalTokens:      request.TotalTokens,
			},
			Metadata: map[string]string{
				"provider.id":                      request.ProviderID,
				"provider.endpoint.name":           request.ProviderEndpointName,
				"provider.endpoint.type":           request.ProviderEndpointType,
				"provider.status":                  request.Status,
				"provider.error":                   request.Error,
				"provider.failure_class":           request.FailureClass,
				"provider.latency_ms":              request.LatencyMS,
				"provider.first_token_latency_ms":  request.FirstTokenLatencyMS,
				"provider.retry_count":             strconv.Itoa(request.RetryCount),
				"provider.fallback_count":          strconv.Itoa(request.FallbackCount),
				"provider.cache.status":            request.CacheStatus,
				"provider.cache.kind":              request.CacheKind,
				"usage.estimated":                  strconv.FormatBool(request.UsageEstimated),
				"model_catalog.version":            request.CatalogVersion,
				"model_catalog.pricing_key":        request.PricingKey,
				"model_catalog.input_cost_per_1m":  request.InputCostPer1M,
				"model_catalog.output_cost_per_1m": request.OutputCostPer1M,
				"model_catalog.currency":           request.Currency,
				"provider.upstream_model":          request.UpstreamModel,
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
	log.Fatal(http.ListenAndServe(":8083", nil))
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
	RequestID             string   `json:"request_id,omitempty"`
	SessionID             string   `json:"session_id,omitempty"`
	TraceID               string   `json:"trace_id,omitempty"`
	CredentialID          string   `json:"credential_id,omitempty"`
	UserID                string   `json:"user_id,omitempty"`
	TeamID                string   `json:"team_id,omitempty"`
	OrganizationID        string   `json:"organization_id,omitempty"`
	Roles                 []string `json:"roles,omitempty"`
	Provider              string   `json:"provider,omitempty"`
	ProviderID            string   `json:"provider_id,omitempty"`
	ProviderEndpointName  string   `json:"provider_endpoint_name,omitempty"`
	ProviderEndpointType  string   `json:"provider_endpoint_type,omitempty"`
	Model                 string   `json:"model,omitempty"`
	UpstreamModel         string   `json:"upstream_model,omitempty"`
	APIType               string   `json:"api_type"`
	Phase                 string   `json:"phase"`
	Status                string   `json:"status,omitempty"`
	Error                 string   `json:"error,omitempty"`
	FailureClass          string   `json:"failure_class,omitempty"`
	LatencyMS             string   `json:"latency_ms,omitempty"`
	FirstTokenLatencyMS   string   `json:"first_token_latency_ms,omitempty"`
	RetryCount            int      `json:"retry_count"`
	FallbackCount         int      `json:"fallback_count"`
	CacheStatus           string   `json:"cache_status,omitempty"`
	CacheKind             string   `json:"cache_kind,omitempty"`
	UsageEstimated        bool     `json:"usage_estimated"`
	PromptTokensEstimated int      `json:"prompt_tokens_estimated"`
	InputTokens           int      `json:"input_tokens"`
	OutputTokens          int      `json:"output_tokens"`
	TotalTokens           int      `json:"total_tokens"`
	CatalogVersion        string   `json:"catalog_version,omitempty"`
	PricingKey            string   `json:"pricing_key,omitempty"`
	InputCostPer1M        string   `json:"input_cost_per_1m,omitempty"`
	OutputCostPer1M       string   `json:"output_cost_per_1m,omitempty"`
	Currency              string   `json:"currency,omitempty"`
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
