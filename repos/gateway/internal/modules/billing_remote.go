package modules

import (
	"context"
	"net/http"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

type UsageRequest struct {
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

type UsageResponse struct {
	Usage    *openai.Usage     `json:"usage,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type RemoteBillingModule struct {
	required bool
	endpoint string
	client   *http.Client
}

func NewRemoteBillingModule(required bool, endpoint string) RemoteBillingModule {
	return RemoteBillingModule{required: required, endpoint: endpoint, client: newRemoteHTTPClient()}
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
	request.Phase = phase
	response, err := callRemote[UsageRequest, UsageResponse](ctx, m.client, m.endpoint, request)
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
		CredentialID:          req.CredentialID,
		UserID:                req.UserID,
		TeamID:                req.TeamID,
		Roles:                 append([]string(nil), req.Roles...),
		Provider:              req.Request.Provider,
		Model:                 req.Request.Model,
		APIType:               "chat_completions",
		Phase:                 "reserve",
		ProviderEndpointName:  metadataValue(req.Metadata, "provider.endpoint.name"),
		ProviderEndpointType:  metadataValue(req.Metadata, "provider.endpoint.type"),
		Status:                metadataValue(req.Metadata, "provider.status"),
		Error:                 metadataValue(req.Metadata, "provider.error"),
		LatencyMS:             metadataValue(req.Metadata, "provider.latency_ms"),
		CacheStatus:           metadataValue(req.Metadata, "provider.cache.status"),
		PromptTokensEstimated: estimateRequestTokens(req),
	}
	request.InputTokens = request.PromptTokensEstimated
	request.OutputTokens = requestedOutputTokens(req)
	request.TotalTokens = request.InputTokens + request.OutputTokens
	if req.ResponseRequest != nil {
		request.Provider = req.ResponseRequest.Provider
		request.Model = req.ResponseRequest.Model
		request.APIType = "responses"
	}
	if req.Response != nil {
		request.Phase = "commit"
		request.InputTokens = req.Response.Usage.PromptTokens
		request.OutputTokens = req.Response.Usage.CompletionTokens
		request.TotalTokens = req.Response.Usage.TotalTokens
		if req.Response.Model != "" {
			request.Model = req.Response.Model
		}
	}
	if req.ResponsesResponse != nil {
		request.Phase = "commit"
		request.InputTokens = req.ResponsesResponse.Usage.InputTokens
		request.OutputTokens = req.ResponsesResponse.Usage.OutputTokens
		request.TotalTokens = req.ResponsesResponse.Usage.TotalTokens
		if req.ResponsesResponse.Model != "" {
			request.Model = req.ResponsesResponse.Model
		}
	}
	if request.TotalTokens == 0 && request.CacheStatus != "hit" {
		request.InputTokens = request.PromptTokensEstimated
		request.TotalTokens = request.PromptTokensEstimated
	}
	return request
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
	return total
}

func metadataValue(metadata map[string]string, key string) string {
	if metadata == nil {
		return ""
	}
	return metadata[key]
}
