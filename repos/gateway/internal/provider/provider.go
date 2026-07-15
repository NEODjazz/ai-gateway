package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type Client interface {
	ChatCompletions(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error)
	Responses(ctx context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error)
}

type Provider interface {
	ChatCompletions(ctx context.Context, req modules.RequestContext) (openai.ChatCompletionResponse, error)
	StreamChatCompletions(ctx context.Context, req modules.RequestContext, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, bool, error)
	Responses(ctx context.Context, req modules.RequestContext) (openai.ResponseResponse, error)
	StreamResponses(ctx context.Context, req modules.RequestContext, write ResponseStreamWriter) (openai.ResponseResponse, bool, error)
	Models() []openai.Model
}

type ChatCompletionStreamWriter func(payload string) error
type ResponseStreamWriter func(event string, payload string) error

type StreamingClient interface {
	StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error)
}

type StreamingResponseClient interface {
	StreamResponses(ctx context.Context, request openai.ResponseRequest, write ResponseStreamWriter) (openai.ResponseResponse, error)
}

var ErrStreamingUnsupported = errors.New("streaming unsupported")

type Config struct {
	Default   string
	Endpoints []config.ProviderEndpointConfig
	Modules   modules.Pipeline
}

type Endpoint struct {
	Name       string
	Type       string
	Models     []string
	Priority   int
	DLPEnabled bool
	AVEnabled  bool
	Provider   Client
}

type Router struct {
	defaultProvider string
	endpoints       []Endpoint
	modules         modules.Pipeline
}

func New(cfg Config) Provider {
	endpoints := make([]Endpoint, 0, len(cfg.Endpoints))
	for _, endpoint := range cfg.Endpoints {
		if endpoint.Enabled != nil && !*endpoint.Enabled {
			continue
		}

		provider := providerFor(endpoint)
		if provider == nil {
			continue
		}

		endpoints = append(endpoints, Endpoint{
			Name:       endpoint.Name,
			Type:       endpoint.Type,
			Models:     endpoint.Models,
			Priority:   endpoint.Priority,
			DLPEnabled: endpoint.DLPEnabled,
			AVEnabled:  endpoint.AVEnabled,
			Provider:   provider,
		})
	}

	if len(endpoints) == 0 {
		endpoints = append(endpoints, Endpoint{Name: "demo", Type: "demo", Provider: Demo{}})
	}

	sort.SliceStable(endpoints, func(i, j int) bool {
		return endpoints[i].Priority < endpoints[j].Priority
	})

	return Router{
		defaultProvider: cfg.Default,
		endpoints:       endpoints,
		modules:         cfg.Modules,
	}
}

func (r Router) ChatCompletions(ctx context.Context, req modules.RequestContext) (openai.ChatCompletionResponse, error) {
	request := req.Request
	candidates := r.candidates(request)
	if len(candidates) == 0 {
		return openai.ChatCompletionResponse{}, fmt.Errorf("no provider endpoint for provider=%q model=%q", request.Provider, request.Model)
	}

	var errs []error
	for _, endpoint := range candidates {
		attemptCtx := providerAttemptContext(req, endpoint)
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			errs = append(errs, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err))
			continue
		}

		response, err := endpoint.Provider.ChatCompletions(ctx, attemptCtx.Request)
		if err == nil {
			mergeChatUsage(&response, attemptCtx.Usage)
			attemptCtx.Response = &response
			if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
				errs = append(errs, fmt.Errorf("%s/%s post-response modules failed: %w", endpoint.Type, endpoint.Name, err))
				continue
			}
			modules.DeanonymizeResponse(&attemptCtx, &response)
			return response, nil
		}
		errs = append(errs, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err))
	}

	return openai.ChatCompletionResponse{}, errors.Join(errs...)
}

func (r Router) StreamChatCompletions(ctx context.Context, req modules.RequestContext, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, bool, error) {
	request := req.Request
	request.Stream = true
	candidates := r.candidates(request)
	if len(candidates) == 0 {
		return openai.ChatCompletionResponse{}, false, fmt.Errorf("no provider endpoint for provider=%q model=%q", request.Provider, request.Model)
	}

	var errs []error
	for _, endpoint := range candidates {
		streamingProvider, ok := endpoint.Provider.(StreamingClient)
		if !ok {
			continue
		}

		attemptCtx := providerAttemptContext(req, endpoint)
		attemptCtx.Request.Stream = true
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			errs = append(errs, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err))
			continue
		}

		response, err := streamingProvider.StreamChatCompletions(ctx, attemptCtx.Request, write)
		if errors.Is(err, ErrStreamingUnsupported) {
			continue
		}
		if err != nil {
			return openai.ChatCompletionResponse{}, true, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err)
		}

		mergeChatUsage(&response, attemptCtx.Usage)
		attemptCtx.Response = &response
		if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
			return openai.ChatCompletionResponse{}, true, fmt.Errorf("%s/%s post-response modules failed: %w", endpoint.Type, endpoint.Name, err)
		}
		modules.DeanonymizeResponse(&attemptCtx, &response)
		return response, true, nil
	}

	if len(errs) > 0 {
		return openai.ChatCompletionResponse{}, false, errors.Join(errs...)
	}
	return openai.ChatCompletionResponse{}, false, nil
}

func (r Router) Responses(ctx context.Context, req modules.RequestContext) (openai.ResponseResponse, error) {
	if req.ResponseRequest == nil {
		return openai.ResponseResponse{}, errors.New("missing response request")
	}
	request := *req.ResponseRequest
	candidates := r.responseCandidates(request)
	if len(candidates) == 0 {
		return openai.ResponseResponse{}, fmt.Errorf("no provider endpoint for provider=%q model=%q", request.Provider, request.Model)
	}

	var errs []error
	for _, endpoint := range candidates {
		attemptCtx := providerAttemptContext(req, endpoint)
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			errs = append(errs, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err))
			continue
		}

		response, err := endpoint.Provider.Responses(ctx, *attemptCtx.ResponseRequest)
		if err == nil {
			mergeResponseUsage(&response, attemptCtx.Usage)
			attemptCtx.ResponsesResponse = &response
			if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
				errs = append(errs, fmt.Errorf("%s/%s post-response modules failed: %w", endpoint.Type, endpoint.Name, err))
				continue
			}
			modules.DeanonymizeResponsesResponse(&attemptCtx, &response)
			return response, nil
		}
		errs = append(errs, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err))
	}

	return openai.ResponseResponse{}, errors.Join(errs...)
}

func (r Router) StreamResponses(ctx context.Context, req modules.RequestContext, write ResponseStreamWriter) (openai.ResponseResponse, bool, error) {
	if req.ResponseRequest == nil {
		return openai.ResponseResponse{}, false, errors.New("missing response request")
	}
	request := *req.ResponseRequest
	request.Stream = true
	candidates := r.responseCandidates(request)
	if len(candidates) == 0 {
		return openai.ResponseResponse{}, false, fmt.Errorf("no provider endpoint for provider=%q model=%q", request.Provider, request.Model)
	}

	var errs []error
	for _, endpoint := range candidates {
		streamingProvider, ok := endpoint.Provider.(StreamingResponseClient)
		if !ok {
			continue
		}

		attemptCtx := providerAttemptContext(req, endpoint)
		attemptCtx.ResponseRequest.Stream = true
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			errs = append(errs, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err))
			continue
		}

		response, err := streamingProvider.StreamResponses(ctx, *attemptCtx.ResponseRequest, write)
		if errors.Is(err, ErrStreamingUnsupported) {
			continue
		}
		if err != nil {
			return openai.ResponseResponse{}, true, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err)
		}

		mergeResponseUsage(&response, attemptCtx.Usage)
		attemptCtx.ResponsesResponse = &response
		if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
			return openai.ResponseResponse{}, true, fmt.Errorf("%s/%s post-response modules failed: %w", endpoint.Type, endpoint.Name, err)
		}
		modules.DeanonymizeResponsesResponse(&attemptCtx, &response)
		return response, true, nil
	}

	if len(errs) > 0 {
		return openai.ResponseResponse{}, false, errors.Join(errs...)
	}
	return openai.ResponseResponse{}, false, nil
}

func (r Router) Models() []openai.Model {
	seen := map[string]bool{}
	models := make([]openai.Model, 0)
	for _, endpoint := range r.endpoints {
		modelIDs := endpoint.Models
		if len(modelIDs) == 0 {
			modelIDs = []string{endpoint.Name}
		}
		for _, modelID := range modelIDs {
			if modelID == "" || seen[modelID] {
				continue
			}
			seen[modelID] = true
			models = append(models, openai.Model{
				ID:      modelID,
				Object:  "model",
				OwnedBy: endpoint.Name,
			})
		}
	}
	sort.SliceStable(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})
	return models
}

func providerAttemptContext(req modules.RequestContext, endpoint Endpoint) modules.RequestContext {
	attemptCtx := req
	attemptCtx.Request = req.Request
	if req.ResponseRequest != nil {
		responseRequest := *req.ResponseRequest
		attemptCtx.ResponseRequest = &responseRequest
	}
	attemptCtx.Response = nil
	attemptCtx.ResponsesResponse = nil
	attemptCtx.Usage = nil
	attemptCtx.AnonymizationValues = nil
	attemptCtx.Metadata = cloneMetadata(req.Metadata)
	for key, value := range providerMetadata(endpoint) {
		attemptCtx.Metadata[key] = value
	}
	return attemptCtx
}

func cloneMetadata(metadata map[string]string) map[string]string {
	cloned := map[string]string{}
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func providerMetadata(endpoint Endpoint) map[string]string {
	return map[string]string{
		"provider.endpoint.name":       endpoint.Name,
		"provider.endpoint.type":       endpoint.Type,
		"provider.modules.dlp.enabled": boolString(endpoint.DLPEnabled),
		"provider.modules.av.enabled":  boolString(endpoint.AVEnabled),
	}
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func mergeChatUsage(response *openai.ChatCompletionResponse, usage *openai.Usage) {
	if usage == nil || response.Usage.PromptTokens != 0 {
		return
	}
	response.Usage.PromptTokens = usage.PromptTokens
	response.Usage.TotalTokens += usage.PromptTokens
}

func mergeResponseUsage(response *openai.ResponseResponse, usage *openai.Usage) {
	if usage == nil || response.Usage.InputTokens != 0 {
		return
	}
	response.Usage.InputTokens = usage.PromptTokens
	response.Usage.TotalTokens += usage.PromptTokens
}

func (r Router) candidates(request openai.ChatCompletionRequest) []Endpoint {
	requestedProvider := strings.TrimSpace(request.Provider)
	filterByProvider := requestedProvider != ""
	if requestedProvider == "" && strings.TrimSpace(request.Model) == "" {
		requestedProvider = r.defaultProvider
		filterByProvider = requestedProvider != ""
	}

	var candidates []Endpoint
	for _, endpoint := range r.endpoints {
		if filterByProvider && requestedProvider != "auto" && requestedProvider != endpoint.Name && requestedProvider != endpoint.Type {
			continue
		}
		if !endpoint.supportsModel(request.Model) {
			continue
		}
		candidates = append(candidates, endpoint)
	}

	return candidates
}

func (r Router) responseCandidates(request openai.ResponseRequest) []Endpoint {
	chatRequest := openai.ChatCompletionRequest{
		Provider: request.Provider,
		Model:    request.Model,
	}
	return r.candidates(chatRequest)
}

func (e Endpoint) supportsModel(model string) bool {
	if len(e.Models) == 0 {
		return true
	}
	for _, supported := range e.Models {
		if supported == model {
			return true
		}
	}
	return false
}

func providerFor(endpoint config.ProviderEndpointConfig) Client {
	switch endpoint.Type {
	case "ollama":
		return NewOllama(endpoint.BaseURL, endpoint.Stream)
	case "openai", "openai-compatible", "openrouter":
		return NewOpenAICompatible(endpoint.BaseURL, endpoint.APIKey, endpoint.Stream)
	case "anthropic":
		return NewAnthropic(endpoint.BaseURL, endpoint.APIKey, endpoint.Stream)
	case "demo":
		return Demo{}
	default:
		return nil
	}
}
