package modules

import (
	"context"
	"net/http"
	"strconv"

	"ai-gateway-gateway/internal/openai"
	"go.opentelemetry.io/otel/trace"
)

type UsageRequest struct {
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
	InputTokens             int      `json:"input_tokens"`
	OutputTokens            int      `json:"output_tokens"`
	TotalTokens             int      `json:"total_tokens"`
	CacheReadInputTokens    int      `json:"cache_read_input_tokens"`
	CacheWriteInputTokens   int      `json:"cache_write_input_tokens"`
	SearchRequests          int      `json:"search_requests"`
	SearchRequestsEstimated bool     `json:"search_requests_estimated"`
	CatalogVersion          string   `json:"catalog_version,omitempty"`
	PricingKey              string   `json:"pricing_key,omitempty"`
	InputCostPer1M          string   `json:"input_cost_per_1m,omitempty"`
	OutputCostPer1M         string   `json:"output_cost_per_1m,omitempty"`
	SearchCostPer1K         string   `json:"search_cost_per_1k,omitempty"`
	Currency                string   `json:"currency,omitempty"`
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
		SearchCostPer1K:       metadataValue(req.Metadata, "model_catalog.search_cost_per_1k"),
		Currency:              metadataValue(req.Metadata, "model_catalog.currency"),
	}
	request.InputTokens = request.PromptTokensEstimated
	request.OutputTokens = requestedOutputTokens(req)
	switch metadataValue(req.Metadata, "gateway.api_type") {
	case "messages":
		request.APIType = "messages"
	case "generate_content":
		request.APIType = "generate_content"
	case "image_generation":
		request.APIType = "image_generation"
	case "image_edit":
		request.APIType = "image_edit"
	case "image_variation":
		request.APIType = "image_variation"
	}
	if request.OutputTokens == 0 && req.CompletionRequest == nil {
		request.OutputTokens = openai.DefaultOutputTokenReserve
	}
	request.TotalTokens = openai.ReserveTokens(request.InputTokens, request.OutputTokens)
	if req.Request.WebSearchOptions != nil {
		request.SearchRequests = openai.WebSearchMaxUses
		request.SearchRequestsEstimated = true
	}
	if req.CompletionRequest != nil {
		request.Provider = req.CompletionRequest.Provider
		request.Model = req.CompletionRequest.Model
		request.APIType = "completions"
		request.InputTokens = openai.CompletionInputTokens(*req.CompletionRequest)
		request.PromptTokensEstimated = request.InputTokens
		request.TotalTokens = openai.CompletionReserveTokens(*req.CompletionRequest)
		request.OutputTokens = request.TotalTokens - request.InputTokens
	}
	if req.ResponseRequest != nil {
		request.Provider = req.ResponseRequest.Provider
		request.Model = req.ResponseRequest.Model
		request.APIType = "responses"
		if metadataValue(req.Metadata, "gateway.api_type") == "responses_compact" {
			request.APIType = "responses_compact"
		}
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
	if req.ModerationRequest != nil {
		request.Provider = req.ModerationRequest.Provider
		request.Model = req.ModerationRequest.Model
		request.APIType = "moderations"
		request.OutputTokens = 0
		request.TotalTokens = request.InputTokens
	}
	if req.ImageGenerationRequest != nil {
		request.Provider = req.ImageGenerationRequest.Provider
		request.Model = req.ImageGenerationRequest.Model
		request.APIType = "image_generation"
	}
	if req.ImageEditRequest != nil {
		request.Provider = req.ImageEditRequest.Provider
		request.Model = req.ImageEditRequest.Model
		request.APIType = "image_edit"
	}
	if req.ImageVariationRequest != nil {
		request.Provider = req.ImageVariationRequest.Provider
		request.Model = req.ImageVariationRequest.Model
		request.APIType = "image_variation"
	}
	if req.AudioTranscriptionRequest != nil {
		request.Provider = req.AudioTranscriptionRequest.Provider
		request.Model = req.AudioTranscriptionRequest.Model
		request.APIType = "audio_transcription"
	}
	if req.Response != nil {
		request.Phase = "commit"
		request.InputTokens = req.Response.Usage.PromptTokens
		request.OutputTokens = req.Response.Usage.CompletionTokens
		request.TotalTokens = req.Response.Usage.TotalTokens
		request.UpstreamModel = req.Response.Model
		request.UsageEstimated = request.TotalTokens == 0
		request.SearchRequests = req.Response.Usage.SearchRequests
		request.SearchRequestsEstimated = false
		if details := req.Response.Usage.PromptTokensDetails; details != nil {
			request.CacheReadInputTokens = nonNegative(details.CachedTokens)
			request.CacheWriteInputTokens = nonNegative(firstNonZero(details.CacheWriteTokens, details.CacheCreationTokens))
		}
	}
	if req.CompletionResponse != nil {
		request.Phase = "commit"
		request.InputTokens = req.CompletionResponse.Usage.PromptTokens
		request.OutputTokens = req.CompletionResponse.Usage.CompletionTokens
		request.TotalTokens = req.CompletionResponse.Usage.TotalTokens
		request.UpstreamModel = req.CompletionResponse.Model
		request.UsageEstimated = request.TotalTokens == 0
		if details := req.CompletionResponse.Usage.PromptTokensDetails; details != nil {
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
	if req.CompactedResponse != nil {
		request.Phase = "commit"
		request.InputTokens = req.CompactedResponse.Usage.InputTokens
		request.OutputTokens = req.CompactedResponse.Usage.OutputTokens
		request.TotalTokens = req.CompactedResponse.Usage.TotalTokens
		request.UpstreamModel = request.Model
		request.UsageEstimated = request.TotalTokens == 0
		if details := req.CompactedResponse.Usage.InputTokensDetails; details != nil {
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
	if req.ModerationResponse != nil {
		request.Phase = "commit"
		request.InputTokens = request.PromptTokensEstimated
		request.OutputTokens = 0
		request.TotalTokens = request.InputTokens
		request.UpstreamModel = req.ModerationResponse.Model
		request.UsageEstimated = true
	}
	if req.ImageGenerationResponse != nil {
		request.Phase = "commit"
		if usage := req.ImageGenerationResponse.Usage; usage != nil {
			request.InputTokens = usage.InputTokens
			request.OutputTokens = usage.OutputTokens
			request.TotalTokens = usage.TotalTokens
			request.UsageEstimated = false
		} else {
			request.InputTokens = request.PromptTokensEstimated
			request.TotalTokens = request.InputTokens
			request.UsageEstimated = true
		}
	}
	if req.AudioTranscriptionResponse != nil {
		request.Phase = "commit"
		if usage := req.AudioTranscriptionResponse.Usage; usage != nil {
			request.InputTokens = usage.InputTokens
			request.OutputTokens = usage.OutputTokens
			request.TotalTokens = usage.TotalTokens
			request.UsageEstimated = false
		}
	}
	if originalModel := metadataValue(req.Metadata, "provider.original_model"); originalModel != "" {
		request.Model = originalModel
	}
	if request.CacheStatus == "hit" {
		request.UsageEstimated = false
	}
	providerReportedExactUsage := (req.ImageGenerationResponse != nil && req.ImageGenerationResponse.Usage != nil) || (req.AudioTranscriptionResponse != nil && req.AudioTranscriptionResponse.Usage != nil)
	if request.TotalTokens == 0 && request.CacheStatus != "hit" && !providerReportedExactUsage {
		if req.CompletionRequest != nil {
			request.InputTokens = openai.CompletionInputTokens(*req.CompletionRequest)
			request.TotalTokens = openai.CompletionReserveTokens(*req.CompletionRequest)
			request.OutputTokens = request.TotalTokens - request.InputTokens
		} else {
			request.InputTokens = request.PromptTokensEstimated
			request.TotalTokens = request.PromptTokensEstimated
		}
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
	if req.CompletionRequest != nil {
		return 0
	}
	if req.ResponseRequest != nil {
		return openai.ResponseOutputLimit(*req.ResponseRequest)
	}
	if req.ImageGenerationRequest != nil {
		return openai.ImageGenerationOutputReserve(*req.ImageGenerationRequest)
	}
	if req.ImageEditRequest != nil {
		return openai.ImageGenerationOutputReserve(req.ImageEditRequest.GenerationRequest())
	}
	if req.ImageVariationRequest != nil {
		return openai.ImageGenerationOutputReserve(req.ImageVariationRequest.GenerationRequest())
	}
	if req.AudioTranscriptionRequest != nil {
		return openai.DefaultOutputTokenReserve
	}
	return openai.ChatOutputReserve(req.Request)
}

func estimateRequestTokens(req *RequestContext) int {
	if req.CompletionRequest != nil {
		return openai.CompletionInputTokens(*req.CompletionRequest)
	}
	if req.ResponseRequest != nil {
		return openai.ResponseInputTokens(*req.ResponseRequest)
	}
	if req.EmbeddingRequest != nil {
		return openai.EmbeddingInputTokenCount(req.EmbeddingRequest.Input)
	}
	if req.RerankRequest != nil {
		return openai.EstimateContextTokens(struct {
			Query     string
			Documents []any
		}{req.RerankRequest.Query, req.RerankRequest.Documents})
	}
	if req.ModerationRequest != nil {
		return openai.ModerationInputTokenCount(req.ModerationRequest.Input)
	}
	if req.ImageGenerationRequest != nil {
		return openai.EstimateContextTokens(req.ImageGenerationRequest.Prompt)
	}
	if req.ImageEditRequest != nil {
		return openai.ImageEditInputTokens(*req.ImageEditRequest)
	}
	if req.ImageVariationRequest != nil {
		return openai.ImageVariationInputTokens(*req.ImageVariationRequest)
	}
	if req.AudioTranscriptionRequest != nil {
		return openai.AudioTranscriptionInputTokens(*req.AudioTranscriptionRequest)
	}
	return openai.ChatInputTokens(req.Request)
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
