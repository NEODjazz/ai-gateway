package provider

import (
	"context"
	"errors"
	"fmt"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func (r Router) CanRouteInteraction(ctx context.Context, request openai.InteractionRequest) bool {
	if request.Model == "" {
		return false
	}
	for _, endpoint := range r.routeCandidates(ctx, modules.RequestContext{}, openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model, MaxTokens: request.GenerationConfig.MaxOutputTokens}, requiredInteractionCapabilities(request)...) {
		if _, ok := endpoint.Provider.(InteractionClient); ok {
			return true
		}
	}
	return false
}

func (r Router) Interactions(ctx context.Context, req modules.RequestContext, request openai.InteractionRequest) (openai.InteractionResponse, error) {
	if req.ResponseRequest == nil {
		return openai.InteractionResponse{}, errors.New("missing interaction policy request")
	}
	candidates := r.routeCandidates(ctx, req, openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model, MaxTokens: request.GenerationConfig.MaxOutputTokens}, requiredInteractionCapabilities(request)...)
	if len(candidates) == 0 {
		return openai.InteractionResponse{}, fmt.Errorf("no interaction endpoint for provider=%q model=%q", request.Provider, request.Model)
	}
	var failures []error
	var lastAttempt *modules.RequestContext
	totalRetries, fallbackCount := 0, 0
	progress := newRouteProgress(candidates)
	if progress.initialFailure != nil {
		failures = append(failures, progress.initialFailure)
		fallbackCount = 1
	}
	for index, endpoint := range candidates {
		client, ok := endpoint.Provider.(InteractionClient)
		if !ok || !progress.allows(endpoint) {
			continue
		}
		progress.enter(endpoint)
		attemptCtx := providerAttemptContext(req, endpoint)
		r.applyCatalogPricing(ctx, &attemptCtx, endpoint, request.Model)
		if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
			err := fmt.Errorf("%s/%s has unknown guardrail policy %q", endpoint.Type, endpoint.Name, endpoint.GuardrailPolicy)
			failures = append(failures, err)
			progress.fail(err)
			continue
		}
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			if terminalModuleError(err) || ctx.Err() != nil {
				return openai.InteractionResponse{}, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			failures = append(failures, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			continue
		}
		if attemptCtx.ResponseRequest == nil {
			return openai.InteractionResponse{}, fmt.Errorf("%s/%s modules removed interaction request", endpoint.Type, endpoint.Name)
		}
		providerRequest := request.WithResponseRequest(*attemptCtx.ResponseRequest)
		started := time.Now()
		lastAttempt = &attemptCtx
		response, retries, err := r.callInteraction(ctx, endpoint, client, providerRequest)
		totalRetries += retries
		setAttemptMetadata(&attemptCtx, started, err)
		setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
		if err == nil {
			shared := openai.ResponseFromInteraction(response)
			if err := mergeResponseUsage(&shared, attemptCtx.Usage); err != nil {
				r.modules.RunFailure(ctx, &attemptCtx, err)
				return openai.InteractionResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
			}
			attemptCtx.ResponsesResponse = &shared
			if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
				return openai.InteractionResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
			}
			modules.DeanonymizeResponsesResponse(&attemptCtx, &shared)
			result := openai.InteractionFromResponse(shared)
			result.Agent, result.Updated = response.Agent, response.Updated
			return result, nil
		}
		failures = append(failures, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err))
		progress.fail(err)
		if ctx.Err() != nil || !progress.hasNext(candidates[index+1:]) {
			joined := errors.Join(failures...)
			r.modules.RunFailure(ctx, lastAttempt, joined)
			return openai.InteractionResponse{}, joined
		}
		fallbackCount++
	}
	joined := errors.Join(failures...)
	if lastAttempt != nil {
		r.modules.RunFailure(ctx, lastAttempt, joined)
	}
	return openai.InteractionResponse{}, joined
}

func requiredInteractionCapabilities(request openai.InteractionRequest) []string {
	required := []string{"interactions"}
	if len(request.Tools) > 0 {
		required = append(required, "tools")
	}
	if request.ResponseFormat != nil {
		required = append(required, "structured_output")
	}
	shared, _ := request.NativeResponseRequest()
	if openai.HasResponseImages(shared) {
		required = append(required, "vision")
	}
	if openai.HasResponseAudio(shared) {
		required = append(required, "audio")
	}
	if openai.HasResponseFiles(shared) {
		required = append(required, "file_input")
	}
	return required
}

func (r Router) callInteraction(ctx context.Context, endpoint Endpoint, client InteractionClient, request openai.InteractionRequest) (openai.InteractionResponse, int, error) {
	shared, _ := request.NativeResponseRequest()
	release, err := r.acquireEndpoint(ctx, endpoint, openai.ResponseReserveTokens(shared))
	if err != nil {
		return openai.InteractionResponse{}, 0, err
	}
	defer release()
	if err := r.health.permit(ctx, endpoint); err != nil {
		return openai.InteractionResponse{}, 0, err
	}
	for attempt := 0; attempt <= endpointMaxRetries(endpoint); attempt++ {
		providerCtx, finish := r.startProviderCall(ctx, endpoint, "interactions")
		response, callErr := client.Interactions(providerCtx, request)
		finish(callErr)
		if callErr == nil {
			r.health.success(ctx, endpoint)
			return response, attempt, nil
		}
		if ctx.Err() != nil || attempt >= endpointRetryLimit(endpoint, callErr) || !retrySameEndpointWithPolicy(endpoint, callErr) {
			r.health.failure(ctx, endpoint, callErr)
			return openai.InteractionResponse{}, attempt, callErr
		}
		if waitErr := r.retry.beforeRetry(ctx, callErr, attempt); waitErr != nil {
			r.health.failure(ctx, endpoint, waitErr)
			return openai.InteractionResponse{}, attempt, waitErr
		}
	}
	return openai.InteractionResponse{}, endpointMaxRetries(endpoint), errors.New("interaction retry exhausted")
}
