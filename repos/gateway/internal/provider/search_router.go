package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func (r Router) Search(ctx context.Context, req modules.RequestContext) (openai.SearchResponse, error) {
	if req.SearchRequest == nil {
		return openai.SearchResponse{}, errors.New("missing search request")
	}
	request := *req.SearchRequest
	model, err := request.RoutingModel()
	if err != nil {
		return openai.SearchResponse{}, &Error{Class: FailureClientRequest, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: err}
	}
	if message := request.Validate(); message != "" {
		return openai.SearchResponse{}, &Error{Class: FailureClientRequest, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	candidates := r.routeCandidates(ctx, req, openai.ChatCompletionRequest{Provider: request.Provider, Model: model}, "search")
	if len(candidates) == 0 {
		return openai.SearchResponse{}, fmt.Errorf("no search endpoint for provider=%q model=%q", request.Provider, model)
	}
	var errs []error
	var lastAttempt *modules.RequestContext
	totalRetries, fallbackCount := 0, 0
	progress := newRouteProgress(candidates)
	if progress.initialFailure != nil {
		errs = append(errs, progress.initialFailure)
		fallbackCount = 1
	}
	for candidateIndex, endpoint := range candidates {
		if !progress.allows(endpoint) {
			continue
		}
		client, ok := endpoint.Provider.(SearchClient)
		if !ok || (endpoint.Type != "openai" && endpoint.Type != "openai-compatible" && endpoint.Type != "openrouter") {
			continue
		}
		progress.enter(endpoint)
		attemptCtx := providerAttemptContext(req, endpoint)
		r.applyCatalogPricing(ctx, &attemptCtx, endpoint, model)
		if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
			err := fmt.Errorf("%s/%s has unknown guardrail policy %q", endpoint.Type, endpoint.Name, endpoint.GuardrailPolicy)
			errs = append(errs, err)
			progress.fail(err)
			continue
		}
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			if terminalModuleError(err) || ctx.Err() != nil {
				return openai.SearchResponse{}, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			errs = append(errs, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			continue
		}
		if attemptCtx.SearchRequest == nil || len(attemptCtx.Request.Messages) == 0 {
			return openai.SearchResponse{}, fmt.Errorf("%s/%s modules removed search request", endpoint.Type, endpoint.Name)
		}
		queries := make([]string, len(attemptCtx.Request.Messages))
		for index := range attemptCtx.Request.Messages {
			queries[index] = openai.ContentText(attemptCtx.Request.Messages[index].Content)
		}
		attemptCtx.SearchRequest.SetQueries(queries)
		if message := attemptCtx.SearchRequest.Validate(); message != "" {
			return openai.SearchResponse{}, fmt.Errorf("%s/%s modules returned invalid search request", endpoint.Type, endpoint.Name)
		}
		started := time.Now()
		lastAttempt = &attemptCtx
		response, retries, callErr := r.callSearch(ctx, endpoint, client, *attemptCtx.SearchRequest)
		if callErr == nil {
			resultLimit := 10
			if attemptCtx.SearchRequest.MaxResults != nil {
				resultLimit = *attemptCtx.SearchRequest.MaxResults
			}
			callErr = validateSearchResponse(response, resultLimit)
			response.Model, _ = attemptCtx.SearchRequest.RoutingModel()
			response.Usage.SearchRequests = attemptCtx.SearchRequest.SearchUnits()
		}
		totalRetries += retries
		setAttemptMetadata(&attemptCtx, started, callErr)
		setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
		if callErr == nil {
			attemptCtx.SearchResponse = &response
			attemptCtx.Usage = &response.Usage
			if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
				return openai.SearchResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
			}
			return response, nil
		}
		errs = append(errs, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, callErr))
		progress.fail(callErr)
		if ctx.Err() != nil || !progress.hasNext(candidates[candidateIndex+1:]) {
			joined := errors.Join(errs...)
			r.modules.RunFailure(ctx, lastAttempt, joined)
			return openai.SearchResponse{}, joined
		}
		fallbackCount++
	}
	if len(errs) == 0 {
		errs = append(errs, errors.New("no selected endpoint implements search"))
	}
	joined := errors.Join(errs...)
	if lastAttempt != nil {
		r.modules.RunFailure(ctx, lastAttempt, joined)
	}
	return openai.SearchResponse{}, joined
}

func (r Router) callSearch(ctx context.Context, endpoint Endpoint, client SearchClient, request openai.SearchRequest) (openai.SearchResponse, int, error) {
	release, err := r.acquireEndpoint(ctx, endpoint, openai.SearchReserveTokens(request))
	if err != nil {
		return openai.SearchResponse{}, 0, err
	}
	defer release()
	if err := r.health.permit(ctx, endpoint); err != nil {
		return openai.SearchResponse{}, 0, err
	}
	for attempt := 0; attempt <= endpointMaxRetries(endpoint); attempt++ {
		providerCtx, finish := r.startProviderCall(ctx, endpoint, "search")
		response, callErr := client.Search(providerCtx, request)
		finish(callErr)
		if callErr == nil {
			r.health.success(ctx, endpoint)
			return response, attempt, nil
		}
		if ctx.Err() != nil || attempt >= endpointRetryLimit(endpoint, callErr) || !retrySameEndpointWithPolicy(endpoint, callErr) {
			r.health.failure(ctx, endpoint, callErr)
			return openai.SearchResponse{}, attempt, callErr
		}
		if waitErr := r.retry.beforeRetry(ctx, callErr, attempt); waitErr != nil {
			r.health.failure(ctx, endpoint, waitErr)
			return openai.SearchResponse{}, attempt, waitErr
		}
	}
	return openai.SearchResponse{}, endpointMaxRetries(endpoint), errors.New("search failed")
}
