package modules

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"ai-gateway-gateway/internal/openai"
	"go.opentelemetry.io/otel/trace"
)

type UsageRequest struct {
	RequestID             string   `json:"request_id,omitempty"`
	SessionID             string   `json:"session_id,omitempty"`
	TraceID               string   `json:"trace_id,omitempty"`
	CredentialID          string   `json:"credential_id,omitempty"`
	UserID                string   `json:"user_id,omitempty"`
	TeamID                string   `json:"team_id,omitempty"`
	OrganizationID        string   `json:"organization_id,omitempty"`
	Roles                 []string `json:"roles,omitempty"`
	Tags                  []string `json:"tags,omitempty"`
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
	CacheReadInputTokens  int      `json:"cache_read_input_tokens"`
	CacheWriteInputTokens int      `json:"cache_write_input_tokens"`
	CatalogVersion        string   `json:"catalog_version,omitempty"`
	PricingKey            string   `json:"pricing_key,omitempty"`
	InputCostPer1M        string   `json:"input_cost_per_1m,omitempty"`
	OutputCostPer1M       string   `json:"output_cost_per_1m,omitempty"`
	Currency              string   `json:"currency,omitempty"`
}

type UsageResponse struct {
	Usage    *openai.Usage     `json:"usage,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type RemoteBillingModule struct {
	required bool
	endpoint string
	secret   string
	client   *http.Client
}

func NewRemoteBillingModule(required bool, endpoint string) RemoteBillingModule {
	return RemoteBillingModule{required: required, endpoint: endpoint, client: newRemoteHTTPClient()}
}

func NewRemoteBillingModuleWithSecret(required bool, endpoint, secret string) RemoteBillingModule {
	module := NewRemoteBillingModule(required, endpoint)
	module.secret = secret
	return module
}

func (m RemoteBillingModule) Name() string              { return "billing" }
func (m RemoteBillingModule) Required() bool            { return m.required }
func (m RemoteBillingModule) PostResponseEnabled() bool { return true }
func (m RemoteBillingModule) HandlePostResponse(ctx context.Context, req *RequestContext) error {
	return m.send(ctx, req, "commit")
}

func (m RemoteBillingModule) Handle(ctx context.Context, req *RequestContext) error {
	return m.send(ctx, req, "reserve")
}

func (m RemoteBillingModule) HandleFailure(ctx context.Context, req *RequestContext, cause error) error {
	if req.Metadata == nil {
		req.Metadata = map[string]string{}
	}
	req.Metadata["provider.status"] = "error"
	req.Metadata["provider.error"] = cause.Error()
	return m.send(ctx, req, "cancel")
}

func (m RemoteBillingModule) send(ctx context.Context, req *RequestContext, phase string) error {
	request := billingRequest(req)
	if spanContext := trace.SpanContextFromContext(ctx); spanContext.IsValid() {
		request.TraceID = spanContext.TraceID().String()
	}
	request.Phase = phase
	response, err := callRemoteWithHeaders[UsageRequest, UsageResponse](ctx, m.client, m.endpoint, request, map[string]string{"X-Service-Token": m.secret})
	if err != nil {
		return err
	}
	req.Usage = response.Usage
	if req.Metadata == nil {
		req.Metadata = map[string]string{}
	}
	for key, value := range response.Metadata {
		if len(key) >= len("billing.") && key[:len("billing.")] == "billing." {
			req.Metadata[key] = value
		}
	}
	return nil
}

func billingRequest(req *RequestContext) UsageRequest {
	request := UsageRequest{
		RequestID:             req.RequestID,
		SessionID:             req.SessionID,
		CredentialID:          req.CredentialID,
		UserID:                req.UserID,
		TeamID:                req.TeamID,
		OrganizationID:        req.OrganizationID,
		Roles:                 append([]string(nil), req.Roles...),
		Tags:                  append([]string(nil), req.Tags...),
		Provider:              req.Request.Provider,
		ProviderID:            metadataValue(req.Metadata, "provider.id"),
		Model:                 req.Request.Model,
		APIType:               "chat_completions",
		Phase:                 "reserve",
		ProviderEndpointName:  metadataValue(req.Metadata, "provider.endpoint.name"),
		ProviderEndpointType:  metadataValue(req.Metadata, "provider.endpoint.type"),
		Status:                metadataValue(req.Metadata, "provider.status"),
		Error:                 metadataValue(req.Metadata, "provider.error"),
		FailureClass:          metadataValue(req.Metadata, "provider.failure_class"),
		LatencyMS:             metadataValue(req.Metadata, "provider.latency_ms"),
		FirstTokenLatencyMS:   metadataValue(req.Metadata, "provider.first_token_latency_ms"),
		RetryCount:            metadataIntValue(req.Metadata, "provider.retry_count"),
		FallbackCount:         metadataIntValue(req.Metadata, "provider.fallback_count"),
		CacheStatus:           metadataValue(req.Metadata, "provider.cache.status"),
		CacheKind:             metadataValue(req.Metadata, "provider.cache.kind"),
		UsageEstimated:        true,
		PromptTokensEstimated: estimateRequestTokens(req),
		CatalogVersion:        metadataValue(req.Metadata, "model_catalog.version"),
		PricingKey:            metadataValue(req.Metadata, "model_catalog.pricing_key"),
		InputCostPer1M:        metadataValue(req.Metadata, "model_catalog.input_cost_per_1m"),
		OutputCostPer1M:       metadataValue(req.Metadata, "model_catalog.output_cost_per_1m"),
		Currency:              metadataValue(req.Metadata, "model_catalog.currency"),
	}
	request.InputTokens = request.PromptTokensEstimated
	request.OutputTokens = requestedOutputTokens(req)
	request.TotalTokens = request.InputTokens + request.OutputTokens
	if req.ResponseRequest != nil {
		request.Provider = req.ResponseRequest.Provider
		request.Model = req.ResponseRequest.Model
		request.APIType = "responses"
	}
	if req.EmbeddingRequest != nil {
		request.Provider = req.EmbeddingRequest.Provider
		request.Model = req.EmbeddingRequest.Model
		request.APIType = "embeddings"
		request.OutputTokens = 0
		request.TotalTokens = request.InputTokens
	}
	if req.RerankRequest != nil {
		request.Provider = req.RerankRequest.Provider
		request.Model = req.RerankRequest.Model
		request.APIType = "rerank"
		request.OutputTokens = 0
		request.TotalTokens = request.InputTokens
	}
	if req.Response != nil {
		request.Phase = "commit"
		request.InputTokens = req.Response.Usage.PromptTokens
		request.OutputTokens = req.Response.Usage.CompletionTokens
		request.TotalTokens = req.Response.Usage.TotalTokens
		request.UpstreamModel = req.Response.Model
		request.UsageEstimated = request.TotalTokens == 0
		if details := req.Response.Usage.PromptTokensDetails; details != nil {
			request.CacheReadInputTokens = nonNegative(details.CachedTokens)
			request.CacheWriteInputTokens = nonNegative(firstNonZero(details.CacheWriteTokens, details.CacheCreationTokens))
		}
	}
	if req.ResponsesResponse != nil {
		request.Phase = "commit"
		request.InputTokens = req.ResponsesResponse.Usage.InputTokens
		request.OutputTokens = req.ResponsesResponse.Usage.OutputTokens
		request.TotalTokens = req.ResponsesResponse.Usage.TotalTokens
		request.UpstreamModel = req.ResponsesResponse.Model
		request.UsageEstimated = request.TotalTokens == 0
		if details := req.ResponsesResponse.Usage.InputTokensDetails; details != nil {
			request.CacheReadInputTokens = nonNegative(details.CachedTokens)
			request.CacheWriteInputTokens = nonNegative(firstNonZero(details.CacheWriteTokens, details.CacheCreationTokens))
		}
	}
	if req.EmbeddingResponse != nil {
		request.Phase = "commit"
		request.InputTokens = req.EmbeddingResponse.Usage.PromptTokens
		request.OutputTokens = 0
		request.TotalTokens = req.EmbeddingResponse.Usage.TotalTokens
		request.UpstreamModel = req.EmbeddingResponse.Model
		request.UsageEstimated = request.TotalTokens == 0
	}
	if req.RerankResponse != nil {
		request.Phase = "commit"
		if req.RerankResponse.Meta != nil {
			if req.RerankResponse.Meta.Tokens != nil {
				request.InputTokens = req.RerankResponse.Meta.Tokens.InputTokens
				request.OutputTokens = req.RerankResponse.Meta.Tokens.OutputTokens
				request.TotalTokens = request.InputTokens + request.OutputTokens
			}
			if request.TotalTokens == 0 && req.RerankResponse.Meta.BilledUnits != nil {
				request.TotalTokens = req.RerankResponse.Meta.BilledUnits.TotalTokens
				request.InputTokens = request.TotalTokens
			}
		}
		request.UsageEstimated = request.TotalTokens == 0
	}
	if originalModel := metadataValue(req.Metadata, "provider.original_model"); originalModel != "" {
		request.Model = originalModel
	}
	if request.CacheStatus == "hit" {
		request.UsageEstimated = false
	}
	if request.TotalTokens == 0 && request.CacheStatus != "hit" {
		request.InputTokens = request.PromptTokensEstimated
		request.TotalTokens = request.PromptTokensEstimated
	}
	return request
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func requestedOutputTokens(req *RequestContext) int {
	if req.ResponseRequest != nil {
		if req.ResponseRequest.MaxOutputTokens != nil && *req.ResponseRequest.MaxOutputTokens > 0 {
			return *req.ResponseRequest.MaxOutputTokens
		}
		if req.ResponseRequest.MaxTokens != nil && *req.ResponseRequest.MaxTokens > 0 {
			return *req.ResponseRequest.MaxTokens
		}
	}
	if req.Request.MaxTokens != nil && *req.Request.MaxTokens > 0 {
		return *req.Request.MaxTokens
	}
	return 0
}

func estimateRequestTokens(req *RequestContext) int {
	total := 0
	for _, message := range req.Request.Messages {
		total += len(strings.Fields(openai.ContentText(message.Content)))
	}
	if req.ResponseRequest != nil {
		total += len(strings.Fields(openai.ContentText(req.ResponseRequest.Input)))
		total += len(strings.Fields(req.ResponseRequest.Instructions))
	}
	if req.EmbeddingRequest != nil {
		total += len(strings.Fields(openai.EmbeddingInputText(req.EmbeddingRequest.Input)))
	}
	if req.RerankRequest != nil {
		if text, ok := openai.RerankDocumentText(*req.RerankRequest); ok {
			total += len(strings.Fields(text))
		}
	}
	return total
}

func metadataValue(metadata map[string]string, key string) string {
	if metadata == nil {
		return ""
	}
	return metadata[key]
}

func metadataIntValue(metadata map[string]string, key string) int {
	value, _ := strconv.Atoi(metadataValue(metadata, key))
	return value
}
