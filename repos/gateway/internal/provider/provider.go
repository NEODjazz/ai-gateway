package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modelcatalog"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
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

type EmbeddingProvider interface {
	Embeddings(ctx context.Context, req modules.RequestContext) (openai.EmbeddingResponse, error)
}

type EmbeddingClient interface {
	Embeddings(ctx context.Context, request openai.EmbeddingRequest) (openai.EmbeddingResponse, error)
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
	Default           string
	Endpoints         []config.ProviderEndpointConfig
	GuardrailPolicies map[string]config.GuardrailPolicyConfig
	Modules           modules.Pipeline
	CacheTTL          time.Duration
	CacheMaxBytes     int
	CacheStore        ExactCacheStore
	Catalog           modelcatalog.Catalog
	Observer          ProviderObserver
}

type ProviderObserver interface {
	ObserveProvider(endpoint, providerType, operation, result string, duration time.Duration)
	ObserveCache(operation, result string)
}

type Endpoint struct {
	Name                  string
	Type                  string
	Models                []string
	Priority              int
	DLPEnabled            bool
	AVEnabled             bool
	MaxRetries            int
	CooldownAfterFailures int
	Cooldown              time.Duration
	GuardrailPolicy       string
	GuardrailPolicyValid  bool
	ModelAliases          map[string]string
	Weight                int
	Capabilities          []string
	Provider              Client
}

type Router struct {
	defaultProvider string
	endpoints       []Endpoint
	modules         modules.Pipeline
	health          *endpointHealthTracker
	routeCounter    *atomic.Uint64
	cache           responseCache
	catalog         modelcatalog.Catalog
	observer        ProviderObserver
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

		dlpEnabled := endpoint.DLPEnabled
		avEnabled := endpoint.AVEnabled
		policyValid := true
		if endpoint.GuardrailPolicy != "" {
			policy, found := cfg.GuardrailPolicies[endpoint.GuardrailPolicy]
			policyValid = found
			if found {
				dlpEnabled = policy.DLP
				avEnabled = policy.AV
			}
		}
		endpoints = append(endpoints, Endpoint{
			Name:                  endpoint.Name,
			Type:                  endpoint.Type,
			Models:                endpoint.Models,
			Priority:              endpoint.Priority,
			DLPEnabled:            dlpEnabled,
			AVEnabled:             avEnabled,
			MaxRetries:            endpoint.MaxRetries,
			CooldownAfterFailures: endpoint.CooldownAfterFailures,
			Cooldown:              time.Duration(endpoint.CooldownSeconds) * time.Second,
			GuardrailPolicy:       endpoint.GuardrailPolicy,
			GuardrailPolicyValid:  policyValid,
			ModelAliases:          endpoint.ModelAliases,
			Weight:                endpoint.Weight,
			Capabilities:          endpoint.Capabilities,
			Provider:              provider,
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
		health:          newEndpointHealthTracker(),
		routeCounter:    &atomic.Uint64{},
		cache:           newResponseCache(cfg.CacheTTL, cfg.CacheMaxBytes, cfg.CacheStore),
		catalog:         cfg.Catalog,
		observer:        cfg.Observer,
	}
}

func (r Router) ChatCompletions(ctx context.Context, req modules.RequestContext) (openai.ChatCompletionResponse, error) {
	request := req.Request
	candidates := r.candidates(request, requiredChatCapabilities(request, false)...)
	if len(candidates) == 0 {
		return openai.ChatCompletionResponse{}, fmt.Errorf("no provider endpoint for provider=%q model=%q", request.Provider, request.Model)
	}

	var errs []error
	var lastAttempt *modules.RequestContext
	for _, endpoint := range candidates {
		attemptCtx := providerAttemptContext(req, endpoint)
		if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
			errs = append(errs, fmt.Errorf("%s/%s has unknown guardrail policy %q", endpoint.Type, endpoint.Name, endpoint.GuardrailPolicy))
			continue
		}
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			if terminalModuleError(err) || ctx.Err() != nil {
				return openai.ChatCompletionResponse{}, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			errs = append(errs, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err))
			continue
		}

		started := time.Now()
		lastAttempt = &attemptCtx
		cacheKey := providerCacheKey("chat", attemptCtx)
		if payload, found, cacheErr := r.cacheGet(ctx, cacheKey); found {
			if response, ok := decodeCached[openai.ChatCompletionResponse](payload); ok {
				attemptCtx.Metadata["provider.cache.status"] = "hit"
				attemptCtx.Metadata["provider.status"] = "ok"
				attemptCtx.Metadata["provider.latency_ms"] = "0"
				response.Usage = openai.Usage{}
				attemptCtx.Response = &response
				if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
					return openai.ChatCompletionResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
				}
				modules.DeanonymizeResponse(&attemptCtx, &response)
				return response, nil
			}
		} else if cacheErr != nil {
			attemptCtx.Metadata["provider.cache.status"] = "error"
			log.Printf("provider cache get failed: %v", cacheErr)
		}
		response, err := r.callChat(ctx, endpoint, attemptCtx.Request)
		setAttemptMetadata(&attemptCtx, started, err)
		if err == nil {
			attemptCtx.Metadata["provider.cache.status"] = "miss"
			if payload, marshalErr := json.Marshal(response); marshalErr == nil {
				if cacheErr := r.cacheSet(ctx, cacheKey, payload); cacheErr != nil {
					attemptCtx.Metadata["provider.cache.status"] = "error"
					log.Printf("provider cache set failed: %v", cacheErr)
				}
			}
			mergeChatUsage(&response, attemptCtx.Usage)
			attemptCtx.Response = &response
			if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
				return openai.ChatCompletionResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
			}
			modules.DeanonymizeResponse(&attemptCtx, &response)
			return response, nil
		}
		errs = append(errs, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err))
		if ctx.Err() != nil || !tryNextEndpoint(err) {
			joined := errors.Join(errs...)
			r.modules.RunFailure(ctx, lastAttempt, joined)
			return openai.ChatCompletionResponse{}, joined
		}
	}

	joined := errors.Join(errs...)
	if lastAttempt != nil {
		r.modules.RunFailure(ctx, lastAttempt, joined)
	}
	return openai.ChatCompletionResponse{}, joined
}

func (r Router) StreamChatCompletions(ctx context.Context, req modules.RequestContext, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, bool, error) {
	request := req.Request
	request.Stream = true
	candidates := r.candidates(request, requiredChatCapabilities(request, true)...)
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
		if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
			errs = append(errs, fmt.Errorf("%s/%s has unknown guardrail policy %q", endpoint.Type, endpoint.Name, endpoint.GuardrailPolicy))
			continue
		}
		attemptCtx.Request.Stream = true
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			if terminalModuleError(err) || ctx.Err() != nil {
				return openai.ChatCompletionResponse{}, false, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			errs = append(errs, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err))
			continue
		}

		started := time.Now()
		providerCtx, finishProviderCall := r.startProviderCall(ctx, endpoint, "chat.stream")
		response, err := streamingProvider.StreamChatCompletions(providerCtx, attemptCtx.Request, write)
		finishProviderCall(err)
		setAttemptMetadata(&attemptCtx, started, err)
		if errors.Is(err, ErrStreamingUnsupported) {
			continue
		}
		if err != nil {
			r.health.failure(endpoint, err)
			r.modules.RunFailure(ctx, &attemptCtx, err)
			return openai.ChatCompletionResponse{}, true, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err)
		}
		r.health.success(endpoint)

		mergeChatUsage(&response, attemptCtx.Usage)
		attemptCtx.Response = &response
		if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
			return openai.ChatCompletionResponse{}, true, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
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
	candidates := r.responseCandidates(request, requiredResponseCapabilities(request, false)...)
	if len(candidates) == 0 {
		return openai.ResponseResponse{}, fmt.Errorf("no provider endpoint for provider=%q model=%q", request.Provider, request.Model)
	}

	var errs []error
	var lastAttempt *modules.RequestContext
	for _, endpoint := range candidates {
		attemptCtx := providerAttemptContext(req, endpoint)
		if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
			errs = append(errs, fmt.Errorf("%s/%s has unknown guardrail policy %q", endpoint.Type, endpoint.Name, endpoint.GuardrailPolicy))
			continue
		}
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			if terminalModuleError(err) || ctx.Err() != nil {
				return openai.ResponseResponse{}, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			errs = append(errs, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err))
			continue
		}

		started := time.Now()
		lastAttempt = &attemptCtx
		cacheKey := providerCacheKey("responses", attemptCtx)
		if payload, found, cacheErr := r.cacheGet(ctx, cacheKey); found {
			if response, ok := decodeCached[openai.ResponseResponse](payload); ok {
				attemptCtx.Metadata["provider.cache.status"] = "hit"
				attemptCtx.Metadata["provider.status"] = "ok"
				attemptCtx.Metadata["provider.latency_ms"] = "0"
				response.Usage = openai.ResponseUsage{}
				attemptCtx.ResponsesResponse = &response
				if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
					return openai.ResponseResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
				}
				modules.DeanonymizeResponsesResponse(&attemptCtx, &response)
				return response, nil
			}
		} else if cacheErr != nil {
			attemptCtx.Metadata["provider.cache.status"] = "error"
			log.Printf("provider cache get failed: %v", cacheErr)
		}
		response, err := r.callResponses(ctx, endpoint, *attemptCtx.ResponseRequest)
		setAttemptMetadata(&attemptCtx, started, err)
		if err == nil {
			attemptCtx.Metadata["provider.cache.status"] = "miss"
			if payload, marshalErr := json.Marshal(response); marshalErr == nil {
				if cacheErr := r.cacheSet(ctx, cacheKey, payload); cacheErr != nil {
					attemptCtx.Metadata["provider.cache.status"] = "error"
					log.Printf("provider cache set failed: %v", cacheErr)
				}
			}
			mergeResponseUsage(&response, attemptCtx.Usage)
			attemptCtx.ResponsesResponse = &response
			if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
				return openai.ResponseResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
			}
			modules.DeanonymizeResponsesResponse(&attemptCtx, &response)
			return response, nil
		}
		errs = append(errs, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err))
		if ctx.Err() != nil || !tryNextEndpoint(err) {
			joined := errors.Join(errs...)
			r.modules.RunFailure(ctx, lastAttempt, joined)
			return openai.ResponseResponse{}, joined
		}
	}

	joined := errors.Join(errs...)
	if lastAttempt != nil {
		r.modules.RunFailure(ctx, lastAttempt, joined)
	}
	return openai.ResponseResponse{}, joined
}

func (r Router) Embeddings(ctx context.Context, req modules.RequestContext) (openai.EmbeddingResponse, error) {
	if req.EmbeddingRequest == nil {
		return openai.EmbeddingResponse{}, errors.New("missing embedding request")
	}
	request := *req.EmbeddingRequest
	candidates := r.candidates(openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model}, "embeddings")
	if len(candidates) == 0 {
		return openai.EmbeddingResponse{}, fmt.Errorf("no embedding endpoint for provider=%q model=%q", request.Provider, request.Model)
	}

	var errs []error
	var lastAttempt *modules.RequestContext
	for _, endpoint := range candidates {
		client, ok := endpoint.Provider.(EmbeddingClient)
		if !ok {
			continue
		}
		attemptCtx := providerAttemptContext(req, endpoint)
		if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
			errs = append(errs, fmt.Errorf("%s/%s has unknown guardrail policy %q", endpoint.Type, endpoint.Name, endpoint.GuardrailPolicy))
			continue
		}
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			if terminalModuleError(err) || ctx.Err() != nil {
				return openai.EmbeddingResponse{}, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			errs = append(errs, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err))
			continue
		}

		started := time.Now()
		lastAttempt = &attemptCtx
		response, err := r.callEmbeddings(ctx, endpoint, client, *attemptCtx.EmbeddingRequest)
		setAttemptMetadata(&attemptCtx, started, err)
		if err == nil {
			mergeEmbeddingUsage(&response, attemptCtx.Usage)
			attemptCtx.EmbeddingResponse = &response
			if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
				return openai.EmbeddingResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
			}
			return response, nil
		}
		errs = append(errs, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err))
		if ctx.Err() != nil || !tryNextEndpoint(err) {
			joined := errors.Join(errs...)
			r.modules.RunFailure(ctx, lastAttempt, joined)
			return openai.EmbeddingResponse{}, joined
		}
	}
	if len(errs) == 0 {
		errs = append(errs, errors.New("no selected endpoint implements embeddings"))
	}
	joined := errors.Join(errs...)
	if lastAttempt != nil {
		r.modules.RunFailure(ctx, lastAttempt, joined)
	}
	return openai.EmbeddingResponse{}, joined
}

func (r Router) StreamResponses(ctx context.Context, req modules.RequestContext, write ResponseStreamWriter) (openai.ResponseResponse, bool, error) {
	if req.ResponseRequest == nil {
		return openai.ResponseResponse{}, false, errors.New("missing response request")
	}
	request := *req.ResponseRequest
	request.Stream = true
	candidates := r.responseCandidates(request, requiredResponseCapabilities(request, true)...)
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
		if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
			errs = append(errs, fmt.Errorf("%s/%s has unknown guardrail policy %q", endpoint.Type, endpoint.Name, endpoint.GuardrailPolicy))
			continue
		}
		attemptCtx.ResponseRequest.Stream = true
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			if terminalModuleError(err) || ctx.Err() != nil {
				return openai.ResponseResponse{}, false, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			errs = append(errs, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err))
			continue
		}

		started := time.Now()
		providerCtx, finishProviderCall := r.startProviderCall(ctx, endpoint, "responses.stream")
		response, err := streamingProvider.StreamResponses(providerCtx, *attemptCtx.ResponseRequest, write)
		finishProviderCall(err)
		setAttemptMetadata(&attemptCtx, started, err)
		if errors.Is(err, ErrStreamingUnsupported) {
			continue
		}
		if err != nil {
			r.health.failure(endpoint, err)
			r.modules.RunFailure(ctx, &attemptCtx, err)
			return openai.ResponseResponse{}, true, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err)
		}
		r.health.success(endpoint)

		mergeResponseUsage(&response, attemptCtx.Usage)
		attemptCtx.ResponsesResponse = &response
		if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
			return openai.ResponseResponse{}, true, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
		}
		modules.DeanonymizeResponsesResponse(&attemptCtx, &response)
		return response, true, nil
	}

	if len(errs) > 0 {
		return openai.ResponseResponse{}, false, errors.Join(errs...)
	}
	return openai.ResponseResponse{}, false, nil
}

func terminalModuleError(err error) bool {
	return errors.Is(err, modules.ErrContentRejected) || errors.Is(err, modules.ErrBudgetExceeded) || errors.Is(err, modules.ErrBillingConflict)
}

func (r Router) Models() []openai.Model {
	seen := map[string]bool{}
	models := make([]openai.Model, 0)
	for _, endpoint := range r.endpoints {
		modelIDs := append([]string(nil), endpoint.Models...)
		for alias := range endpoint.ModelAliases {
			modelIDs = append(modelIDs, alias)
		}
		if len(modelIDs) == 0 {
			modelIDs = []string{endpoint.Name}
		}
		for _, modelID := range modelIDs {
			if modelID == "" || seen[modelID] {
				continue
			}
			lookupModels := []string{modelID}
			if upstream, found := endpoint.ModelAliases[modelID]; found {
				lookupModels = append(lookupModels, upstream)
			}
			if _, found := r.catalog.Find(endpoint.Name, endpoint.Type, lookupModels...); !found && r.catalog.DenyUnknownModels() {
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
	if req.EmbeddingRequest != nil {
		embeddingRequest := *req.EmbeddingRequest
		attemptCtx.EmbeddingRequest = &embeddingRequest
	}
	attemptCtx.Response = nil
	attemptCtx.ResponsesResponse = nil
	attemptCtx.EmbeddingResponse = nil
	attemptCtx.Usage = nil
	attemptCtx.AnonymizationValues = nil
	attemptCtx.Metadata = cloneMetadata(req.Metadata)
	for key, value := range providerMetadata(endpoint) {
		attemptCtx.Metadata[key] = value
	}
	requestedModel := attemptCtx.Request.Model
	if upstreamModel, found := endpoint.ModelAliases[requestedModel]; found {
		attemptCtx.Request.Model = upstreamModel
		if attemptCtx.ResponseRequest != nil {
			attemptCtx.ResponseRequest.Model = upstreamModel
		}
		if attemptCtx.EmbeddingRequest != nil {
			attemptCtx.EmbeddingRequest.Model = upstreamModel
		}
		attemptCtx.Metadata["provider.requested_model"] = requestedModel
		attemptCtx.Metadata["provider.upstream_model"] = upstreamModel
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
		"provider.guardrail.policy":    endpoint.GuardrailPolicy,
		"provider.guardrail.valid":     boolString(endpoint.GuardrailPolicy == "" || endpoint.GuardrailPolicyValid),
	}
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func (r Router) callChat(ctx context.Context, endpoint Endpoint, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	var response openai.ChatCompletionResponse
	var err error
	for attempt := 0; attempt <= endpoint.MaxRetries; attempt++ {
		providerCtx, finishProviderCall := r.startProviderCall(ctx, endpoint, "chat")
		response, err = endpoint.Provider.ChatCompletions(providerCtx, request)
		finishProviderCall(err)
		if err == nil {
			r.health.success(endpoint)
			return response, nil
		}
		if ctx.Err() != nil || attempt == endpoint.MaxRetries || !retrySameEndpoint(err) {
			break
		}
	}
	r.health.failure(endpoint, err)
	return openai.ChatCompletionResponse{}, err
}

func (r Router) callResponses(ctx context.Context, endpoint Endpoint, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	var response openai.ResponseResponse
	var err error
	for attempt := 0; attempt <= endpoint.MaxRetries; attempt++ {
		providerCtx, finishProviderCall := r.startProviderCall(ctx, endpoint, "responses")
		response, err = endpoint.Provider.Responses(providerCtx, request)
		finishProviderCall(err)
		if err == nil {
			r.health.success(endpoint)
			return response, nil
		}
		if ctx.Err() != nil || attempt == endpoint.MaxRetries || !retrySameEndpoint(err) {
			break
		}
	}
	r.health.failure(endpoint, err)
	return openai.ResponseResponse{}, err
}

func (r Router) callEmbeddings(ctx context.Context, endpoint Endpoint, client EmbeddingClient, request openai.EmbeddingRequest) (openai.EmbeddingResponse, error) {
	var response openai.EmbeddingResponse
	var err error
	for attempt := 0; attempt <= endpoint.MaxRetries; attempt++ {
		providerCtx, finishProviderCall := r.startProviderCall(ctx, endpoint, "embeddings")
		response, err = client.Embeddings(providerCtx, request)
		finishProviderCall(err)
		if err == nil {
			r.health.success(endpoint)
			return response, nil
		}
		if ctx.Err() != nil || attempt == endpoint.MaxRetries || !retrySameEndpoint(err) {
			break
		}
	}
	r.health.failure(endpoint, err)
	return openai.EmbeddingResponse{}, err
}

func (r Router) startProviderCall(ctx context.Context, endpoint Endpoint, operation string) (context.Context, func(error)) {
	started := time.Now()
	spanCtx, span := otel.Tracer("ai-gateway/provider").Start(ctx, "provider."+operation,
		trace.WithAttributes(
			attribute.String("ai.provider.endpoint", endpoint.Name),
			attribute.String("ai.provider.type", endpoint.Type),
			attribute.String("ai.operation", operation),
		))
	return spanCtx, func(err error) {
		result := "ok"
		if err != nil {
			result = string(failureClass(err))
			span.RecordError(err)
			span.SetStatus(codes.Error, result)
		}
		span.SetAttributes(attribute.String("ai.result", result))
		span.End()
		if r.observer != nil {
			r.observer.ObserveProvider(endpoint.Name, endpoint.Type, operation, result, time.Since(started))
		}
	}
}

func setAttemptMetadata(req *modules.RequestContext, started time.Time, err error) {
	if req.Metadata == nil {
		req.Metadata = map[string]string{}
	}
	req.Metadata["provider.latency_ms"] = strconv.FormatInt(time.Since(started).Milliseconds(), 10)
	if err == nil {
		req.Metadata["provider.status"] = "ok"
		delete(req.Metadata, "provider.error")
		delete(req.Metadata, "provider.failure_class")
		return
	}
	req.Metadata["provider.status"] = "error"
	req.Metadata["provider.error"] = err.Error()
	req.Metadata["provider.failure_class"] = string(failureClass(err))
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

func mergeEmbeddingUsage(response *openai.EmbeddingResponse, usage *openai.Usage) {
	if usage == nil || response.Usage.PromptTokens != 0 {
		return
	}
	response.Usage.PromptTokens = usage.PromptTokens
	response.Usage.TotalTokens += usage.PromptTokens
}

func (r Router) candidates(request openai.ChatCompletionRequest, capabilities ...string) []Endpoint {
	requestedProvider := strings.TrimSpace(request.Provider)
	filterByProvider := requestedProvider != ""
	if requestedProvider == "" && strings.TrimSpace(request.Model) == "" {
		requestedProvider = r.defaultProvider
		filterByProvider = requestedProvider != ""
	}

	var candidates []Endpoint
	for _, endpoint := range r.endpoints {
		if !r.health.available(endpoint) {
			continue
		}
		if filterByProvider && requestedProvider != "auto" && requestedProvider != endpoint.Name && requestedProvider != endpoint.Type {
			continue
		}
		if !endpoint.supportsModel(request.Model) {
			continue
		}
		if !r.supportsCapabilities(endpoint, request.Model, capabilities...) {
			continue
		}
		if !r.supportsOutputLimit(endpoint, request.Model, request.MaxTokens) {
			continue
		}
		candidates = append(candidates, endpoint)
	}

	return r.weightedOrder(candidates)
}

func (r Router) responseCandidates(request openai.ResponseRequest, capabilities ...string) []Endpoint {
	chatRequest := openai.ChatCompletionRequest{
		Provider:  request.Provider,
		Model:     request.Model,
		MaxTokens: request.MaxOutputTokens,
	}
	if chatRequest.MaxTokens == nil {
		chatRequest.MaxTokens = request.MaxTokens
	}
	return r.candidates(chatRequest, capabilities...)
}

func requiredChatCapabilities(request openai.ChatCompletionRequest, stream bool) []string {
	required := []string{"chat"}
	if stream {
		required = append(required, "stream")
	}
	if len(request.Tools) > 0 {
		required = append(required, "tools")
	}
	if request.ResponseFormat != nil {
		required = append(required, "structured_output")
	}
	return required
}

func requiredResponseCapabilities(request openai.ResponseRequest, stream bool) []string {
	required := []string{"responses"}
	if stream {
		required = append(required, "stream")
	}
	if len(request.Tools) > 0 {
		required = append(required, "tools")
	}
	if request.Text != nil {
		required = append(required, "structured_output")
	}
	return required
}

func (e Endpoint) supportsModel(model string) bool {
	if _, found := e.ModelAliases[model]; found {
		return true
	}
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

func (e Endpoint) supportsCapabilities(required ...string) bool {
	if len(e.Capabilities) == 0 {
		return true
	}
	available := map[string]bool{}
	for _, capability := range e.Capabilities {
		available[capability] = true
	}
	for _, capability := range required {
		if !available[capability] {
			return false
		}
	}
	return true
}

func (r Router) supportsCapabilities(endpoint Endpoint, requestedModel string, required ...string) bool {
	models := []string{requestedModel}
	if upstream, found := endpoint.ModelAliases[requestedModel]; found {
		models = append(models, upstream)
	}
	entry, found := r.catalog.Find(endpoint.Name, endpoint.Type, models...)
	if !found {
		if r.catalog.DenyUnknownModels() {
			return false
		}
		return endpoint.supportsCapabilities(required...)
	}
	if entry.Capabilities == nil {
		return endpoint.supportsCapabilities(required...)
	}
	available := make(map[string]bool, len(entry.Capabilities))
	for _, capability := range entry.Capabilities {
		available[capability] = true
	}
	for _, capability := range required {
		if !available[capability] {
			return false
		}
	}
	return true
}

func (r Router) supportsOutputLimit(endpoint Endpoint, requestedModel string, requested *int) bool {
	if requested == nil || *requested <= 0 {
		return true
	}
	models := []string{requestedModel}
	if upstream, found := endpoint.ModelAliases[requestedModel]; found {
		models = append(models, upstream)
	}
	entry, found := r.catalog.Find(endpoint.Name, endpoint.Type, models...)
	return !found || entry.MaxOutputTokens <= 0 || *requested <= entry.MaxOutputTokens
}

func (r Router) weightedOrder(candidates []Endpoint) []Endpoint {
	if len(candidates) < 2 || r.routeCounter == nil {
		return candidates
	}
	ordered := make([]Endpoint, 0, len(candidates))
	for start := 0; start < len(candidates); {
		end := start + 1
		for end < len(candidates) && candidates[end].Priority == candidates[start].Priority {
			end++
		}
		group := candidates[start:end]
		total := 0
		for _, endpoint := range group {
			weight := endpoint.Weight
			if weight <= 0 {
				weight = 1
			}
			total += weight
		}
		slot := int((r.routeCounter.Add(1) - 1) % uint64(total))
		selected := 0
		for index, endpoint := range group {
			weight := endpoint.Weight
			if weight <= 0 {
				weight = 1
			}
			if slot < weight {
				selected = index
				break
			}
			slot -= weight
		}
		ordered = append(ordered, group[selected])
		ordered = append(ordered, group[:selected]...)
		ordered = append(ordered, group[selected+1:]...)
		start = end
	}
	return ordered
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
