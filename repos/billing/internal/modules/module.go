package modules

import (
	"context"
	"errors"
	"fmt"
	"log"

	"ai-gateway-billing/internal/openai"
)

type RequestContext struct {
	CredentialID            string                         `json:"credential_id,omitempty"`
	RequestID               string                         `json:"request_id,omitempty"`
	SessionID               string                         `json:"session_id,omitempty"`
	TraceID                 string                         `json:"trace_id,omitempty"`
	PromptTokensEstimated   int                            `json:"prompt_tokens_estimated,omitempty"`
	InputCharacters         int                            `json:"input_characters,omitempty"`
	InputPages              int                            `json:"input_pages,omitempty"`
	InputAudioMilliseconds  int                            `json:"input_audio_milliseconds,omitempty"`
	CacheReadInputTokens    int                            `json:"cache_read_input_tokens,omitempty"`
	CacheWriteInputTokens   int                            `json:"cache_write_input_tokens,omitempty"`
	SearchRequests          int                            `json:"search_requests,omitempty"`
	SearchRequestsEstimated bool                           `json:"search_requests_estimated,omitempty"`
	PostResponse            bool                           `json:"post_response,omitempty"`
	BillingPhase            string                         `json:"billing_phase,omitempty"`
	APIType                 string                         `json:"api_type,omitempty"`
	UserID                  string                         `json:"user_id,omitempty"`
	TeamID                  string                         `json:"team_id,omitempty"`
	OrganizationID          string                         `json:"organization_id,omitempty"`
	Roles                   []string                       `json:"roles,omitempty"`
	Tags                    []string                       `json:"tags,omitempty"`
	Request                 openai.ChatCompletionRequest   `json:"request"`
	ResponseRequest         *openai.ResponseRequest        `json:"response_request,omitempty"`
	Response                *openai.ChatCompletionResponse `json:"response,omitempty"`
	ResponsesResponse       *openai.ResponseResponse       `json:"responses_response,omitempty"`
	Usage                   *openai.Usage                  `json:"usage,omitempty"`
	BillingEvent            *BillingEvent                  `json:"billing_event,omitempty"`
	Metadata                map[string]string              `json:"metadata,omitempty"`
	AnonymizationValues     map[string]string              `json:"anonymization_values,omitempty"`
}

type BillingEvent struct {
	EventID                 string   `json:"event_id,omitempty"`
	RequestID               string   `json:"request_id,omitempty"`
	SessionID               string   `json:"session_id,omitempty"`
	TraceID                 string   `json:"trace_id,omitempty"`
	UserID                  string   `json:"user_id,omitempty"`
	TeamID                  string   `json:"team_id,omitempty"`
	OrganizationID          string   `json:"organization_id,omitempty"`
	Roles                   []string `json:"roles,omitempty"`
	Tags                    []string `json:"tags,omitempty"`
	APIKeyFingerprint       string   `json:"api_key_fingerprint,omitempty"`
	Provider                string   `json:"provider,omitempty"`
	ProviderID              string   `json:"provider_id,omitempty"`
	ProviderEndpointName    string   `json:"provider_endpoint_name,omitempty"`
	ProviderEndpointType    string   `json:"provider_endpoint_type,omitempty"`
	Model                   string   `json:"model,omitempty"`
	UpstreamModel           string   `json:"upstream_model,omitempty"`
	APIType                 string   `json:"api_type,omitempty"`
	Phase                   string   `json:"phase"`
	Status                  string   `json:"status,omitempty"`
	Error                   string   `json:"error,omitempty"`
	FailureClass            string   `json:"failure_class,omitempty"`
	LatencyMS               int      `json:"latency_ms,omitempty"`
	FirstTokenLatencyMS     int      `json:"first_token_latency_ms,omitempty"`
	RetryCount              int      `json:"retry_count"`
	FallbackCount           int      `json:"fallback_count"`
	CacheStatus             string   `json:"cache_status,omitempty"`
	CacheKind               string   `json:"cache_kind,omitempty"`
	UsageEstimated          bool     `json:"usage_estimated"`
	PromptTokensEstimated   int      `json:"prompt_tokens_estimated"`
	InputCharacters         int      `json:"input_characters"`
	InputPages              int      `json:"input_pages"`
	InputAudioMilliseconds  int      `json:"input_audio_milliseconds"`
	InputTokens             int      `json:"input_tokens"`
	OutputTokens            int      `json:"output_tokens"`
	TotalTokens             int      `json:"total_tokens"`
	CacheReadInputTokens    int      `json:"cache_read_input_tokens"`
	CacheWriteInputTokens   int      `json:"cache_write_input_tokens"`
	SearchRequests          int      `json:"search_requests"`
	SearchRequestsEstimated bool     `json:"search_requests_estimated"`
	Cost                    float64  `json:"cost"`
	Currency                string   `json:"currency,omitempty"`
	CatalogVersion          string   `json:"catalog_version,omitempty"`
	PricingKey              string   `json:"pricing_key,omitempty"`
	InputCostPer1M          float64  `json:"input_cost_per_1m,omitempty"`
	OutputCostPer1M         float64  `json:"output_cost_per_1m,omitempty"`
	SearchCostPer1K         float64  `json:"search_cost_per_1k,omitempty"`
	CharacterCostPer1M      float64  `json:"character_cost_per_1m,omitempty"`
	PageCostPer1K           float64  `json:"page_cost_per_1k,omitempty"`
	AudioCostPerMinute      float64  `json:"audio_cost_per_minute,omitempty"`
	Timestamp               string   `json:"timestamp"`
}

type Module interface {
	Name() string
	Required() bool
	Handle(ctx context.Context, req *RequestContext) error
}

type Pipeline struct {
	modules []Module
}

func NewPipeline(modules []Module) Pipeline {
	return Pipeline{modules: modules}
}

func (p Pipeline) Run(ctx context.Context, req *RequestContext) error {
	for _, module := range p.modules {
		if err := module.Handle(ctx, req); err != nil {
			if module.Required() {
				return fmt.Errorf("%s module failed: %w", module.Name(), err)
			}
			log.Printf("optional module %s skipped after error: %v", module.Name(), err)
		}
	}
	return nil
}

var ErrUnauthorized = errors.New("unauthorized")
var ErrBudgetExceeded = errors.New("budget exceeded")
var ErrBillingConflict = errors.New("billing lifecycle conflict")
