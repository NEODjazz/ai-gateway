package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"ai-gateway-billing/internal/modules"
	"ai-gateway-billing/internal/openai"
)

func main() {
	module := modules.NewBillingModule(true)
	defer module.Close()

	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := module.Ready(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	http.HandleFunc("/livez", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	http.HandleFunc("/usage", func(w http.ResponseWriter, r *http.Request) {
		var request usageRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx := modules.RequestContext{
			RequestID:             request.RequestID,
			CredentialID:          request.CredentialID,
			UserID:                request.UserID,
			TeamID:                request.TeamID,
			Roles:                 request.Roles,
			PromptTokensEstimated: request.PromptTokensEstimated,
			PostResponse:          request.Phase == "commit",
			BillingPhase:          request.Phase,
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
				"provider.endpoint.name": request.ProviderEndpointName,
				"provider.endpoint.type": request.ProviderEndpointType,
				"provider.status":        request.Status,
				"provider.error":         request.Error,
				"provider.latency_ms":    request.LatencyMS,
				"provider.cache.status":  request.CacheStatus,
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

type usageRequest struct {
	RequestID             string   `json:"request_id,omitempty"`
	CredentialID          string   `json:"credential_id,omitempty"`
	UserID                string   `json:"user_id,omitempty"`
	TeamID                string   `json:"team_id,omitempty"`
	Roles                 []string `json:"roles,omitempty"`
	Provider              string   `json:"provider,omitempty"`
	ProviderEndpointName  string   `json:"provider_endpoint_name,omitempty"`
	ProviderEndpointType  string   `json:"provider_endpoint_type,omitempty"`
	Model                 string   `json:"model,omitempty"`
	APIType               string   `json:"api_type"`
	Phase                 string   `json:"phase"`
	Status                string   `json:"status,omitempty"`
	Error                 string   `json:"error,omitempty"`
	LatencyMS             string   `json:"latency_ms,omitempty"`
	CacheStatus           string   `json:"cache_status,omitempty"`
	PromptTokensEstimated int      `json:"prompt_tokens_estimated"`
	InputTokens           int      `json:"input_tokens"`
	OutputTokens          int      `json:"output_tokens"`
	TotalTokens           int      `json:"total_tokens"`
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
