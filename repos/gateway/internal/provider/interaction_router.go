package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func (r Router) StreamInteractions(ctx context.Context, req modules.RequestContext, request openai.InteractionRequest, write ResponseStreamWriter) (openai.InteractionResponse, bool, error) {
	if req.ResponseRequest == nil {
		return openai.InteractionResponse{}, true, errors.New("missing interaction policy request")
	}
	if err := r.validateResponseOwnership(req, *req.ResponseRequest); err != nil {
		return openai.InteractionResponse{}, true, err
	}
	candidates, err := r.interactionCandidates(ctx, req, request)
	if err != nil {
		return openai.InteractionResponse{}, true, err
	}
	if len(candidates) == 0 || outputDLPRequired(req, candidates) {
		return openai.InteractionResponse{}, false, nil
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
		client, ok := endpoint.Provider.(StreamingInteractionClient)
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
		attemptCtx.ResponseRequest.Stream = true
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			if terminalModuleError(err) || ctx.Err() != nil {
				return openai.InteractionResponse{}, false, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			failures = append(failures, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			continue
		}
		if attemptCtx.ResponseRequest == nil {
			return openai.InteractionResponse{}, false, fmt.Errorf("%s/%s modules removed interaction request", endpoint.Type, endpoint.Name)
		}
		providerRequest := request.WithResponseRequest(*attemptCtx.ResponseRequest)
		providerRequest.Stream = true
		lastAttempt = &attemptCtx
		started := time.Now()
		release, err := r.acquireEndpoint(ctx, endpoint, openai.ResponseReserveTokens(*attemptCtx.ResponseRequest))
		if err != nil {
			setAttemptMetadata(&attemptCtx, started, err)
			setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
			failures = append(failures, fmt.Errorf("%s/%s admission failed: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			fallbackCount++
			continue
		}
		if err := r.health.permit(ctx, endpoint); err != nil {
			release()
			setAttemptMetadata(&attemptCtx, started, err)
			setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
			failures = append(failures, fmt.Errorf("%s/%s circuit denied call: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			fallbackCount++
			continue
		}
		streamStarted := false
		firstTokenLatency := time.Duration(0)
		var response openai.InteractionResponse
		for retry := 0; ; retry++ {
			tracker := newStreamAttemptTracker(started)
			providerCtx, finish := r.startProviderCall(ctx, endpoint, "interactions.stream")
			response, err = client.StreamInteractions(providerCtx, providerRequest, deanonymizingResponseStreamWriter(attemptCtx.AnonymizationValues, tracker.responseWriter(write)))
			finish(err)
			streamStarted, firstTokenLatency = tracker.state()
			if err == nil || streamStarted || ctx.Err() != nil || retry >= endpointRetryLimit(endpoint, err) || !retrySameEndpointWithPolicy(endpoint, err) {
				break
			}
			if waitErr := r.retry.beforeRetry(ctx, err, retry); waitErr != nil {
				err = waitErr
				break
			}
			totalRetries++
		}
		release()
		setAttemptMetadata(&attemptCtx, started, err)
		setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
		if streamStarted {
			setFirstTokenLatency(&attemptCtx, firstTokenLatency)
		}
		if err != nil {
			if ctx.Err() == nil {
				r.health.failure(ctx, endpoint, err)
			}
			wrapped := fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err)
			progress.fail(err)
			if streamStarted || ctx.Err() != nil || !progress.hasNext(candidates[index+1:]) {
				r.modules.RunFailure(ctx, &attemptCtx, wrapped)
				return openai.InteractionResponse{}, streamStarted, wrapped
			}
			failures = append(failures, wrapped)
			fallbackCount++
			continue
		}
		r.health.success(ctx, endpoint)
		shared := openai.ResponseFromInteraction(response)
		if err := mergeResponseUsage(&shared, attemptCtx.Usage); err != nil {
			r.modules.RunFailure(ctx, &attemptCtx, err)
			return openai.InteractionResponse{}, streamStarted, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
		}
		attemptCtx.ResponsesResponse = &shared
		if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
			return openai.InteractionResponse{}, streamStarted, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
		}
		modules.DeanonymizeResponsesResponse(&attemptCtx, &shared)
		result := openai.InteractionFromResponse(shared)
		result.Agent, result.Updated = response.Agent, response.Updated
		if interactionUsesAgent(request) {
			result.Agent, result.Model = interactionRoutingModel(request), ""
		}
		if err := r.persistInteractionOwnership(ctx, attemptCtx, *attemptCtx.ResponseRequest, interactionRoutingModel(request), interactionUsesAgent(request), result.ID, endpoint); err != nil {
			return openai.InteractionResponse{}, streamStarted, err
		}
		terminal, err := json.Marshal(map[string]any{"event_type": "interaction.completed", "interaction": result})
		if err != nil {
			return openai.InteractionResponse{}, streamStarted, err
		}
		if err := write("interaction.completed", string(terminal)); err != nil {
			return openai.InteractionResponse{}, true, err
		}
		return result, true, nil
	}
	if len(failures) > 0 {
		joined := errors.Join(failures...)
		if lastAttempt != nil {
			r.modules.RunFailure(ctx, lastAttempt, joined)
		}
		return openai.InteractionResponse{}, false, joined
	}
	return openai.InteractionResponse{}, false, nil
}

func (r Router) CanRouteInteraction(ctx context.Context, request openai.InteractionRequest) bool {
	model := interactionRoutingModel(request)
	if model == "" {
		return false
	}
	for _, endpoint := range r.routeCandidates(ctx, modules.RequestContext{}, openai.ChatCompletionRequest{Provider: request.Provider, Model: model, MaxTokens: request.GenerationConfig.MaxOutputTokens}, requiredInteractionCapabilities(request)...) {
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
	if err := r.validateResponseOwnership(req, *req.ResponseRequest); err != nil {
		return openai.InteractionResponse{}, err
	}
	candidates, err := r.interactionCandidates(ctx, req, request)
	if err != nil {
		return openai.InteractionResponse{}, err
	}
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
			if interactionUsesAgent(request) {
				result.Agent, result.Model = interactionRoutingModel(request), ""
			}
			if err := r.persistInteractionOwnership(ctx, attemptCtx, *attemptCtx.ResponseRequest, interactionRoutingModel(request), interactionUsesAgent(request), result.ID, endpoint); err != nil {
				return openai.InteractionResponse{}, err
			}
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

func (r Router) interactionCandidates(ctx context.Context, req modules.RequestContext, request openai.InteractionRequest) ([]Endpoint, error) {
	model := interactionRoutingModel(request)
	if request.PreviousInteractionID == "" {
		return r.routeCandidates(ctx, req, openai.ChatCompletionRequest{Provider: request.Provider, Model: model, MaxTokens: request.GenerationConfig.MaxOutputTokens}, requiredInteractionCapabilities(request)...), nil
	}
	binding, endpoint, err := r.interactionResource(ctx, req, request.PreviousInteractionID)
	if err != nil {
		return nil, err
	}
	providerMismatch := request.Provider != "" && request.Provider != endpoint.Name && request.Provider != endpoint.ProviderID && request.Provider != endpoint.Type
	if binding.Model != model || binding.Agent != interactionUsesAgent(request) || providerMismatch || !endpoint.supportsCapabilities(requiredInteractionCapabilities(request)...) {
		return nil, ErrResponseDeploymentChanged
	}
	return []Endpoint{endpoint}, nil
}

func (r Router) persistInteractionOwnership(ctx context.Context, req modules.RequestContext, request openai.ResponseRequest, model string, agent bool, id string, endpoint Endpoint) error {
	if !persistentResponseRequested(request) {
		return nil
	}
	binding := responseOwnership{Endpoint: endpoint.Name, Model: model, Deployment: responseDeploymentIdentity(endpoint), Resource: "interaction", Agent: agent}
	if err := r.ownership.put(ctx, req, id, binding); err != nil {
		if errors.Is(err, ErrResponseOwnershipConflict) {
			return err
		}
		return ErrResponseOwnershipUnavailable
	}
	return nil
}

func (r Router) interactionResource(ctx context.Context, req modules.RequestContext, id string) (responseOwnership, Endpoint, error) {
	binding, found, err := r.ownership.get(ctx, req, id)
	if err != nil {
		return responseOwnership{}, Endpoint{}, err
	}
	if !found {
		return responseOwnership{}, Endpoint{}, ErrResponseNotFound
	}
	for _, endpoint := range r.runtimeEndpoints() {
		if endpoint.Name != binding.Endpoint {
			continue
		}
		if binding.Resource != "interaction" || responseDeploymentIdentity(endpoint) != binding.Deployment || !endpoint.supportsModel(binding.Model) || !endpoint.supportsCapabilities("interactions") {
			return responseOwnership{}, Endpoint{}, ErrResponseDeploymentChanged
		}
		return binding, endpoint, nil
	}
	return responseOwnership{}, Endpoint{}, ErrResponseDeploymentChanged
}

func (r Router) ResolveInteractionResource(ctx context.Context, req modules.RequestContext, id string) (string, error) {
	binding, _, err := r.interactionResource(ctx, req, id)
	return binding.Model, err
}

func (r Router) RetrieveInteraction(ctx context.Context, req modules.RequestContext, id string) (openai.InteractionResponse, error) {
	binding, endpoint, err := r.interactionResource(ctx, req, id)
	if err != nil {
		return openai.InteractionResponse{}, err
	}
	client, ok := endpoint.Provider.(InteractionResourceClient)
	if !ok {
		return openai.InteractionResponse{}, ErrResponseDeploymentChanged
	}
	result, err := callResponseLifecycle(r, ctx, endpoint, "interactions.retrieve", func(callCtx context.Context) (openai.InteractionResponse, error) {
		return client.RetrieveInteraction(callCtx, id)
	})
	if err == nil {
		setInteractionBinding(&result, binding)
	}
	return result, err
}

func (r Router) CancelInteraction(ctx context.Context, req modules.RequestContext, id string) (openai.InteractionResponse, error) {
	binding, endpoint, err := r.interactionResource(ctx, req, id)
	if err != nil {
		return openai.InteractionResponse{}, err
	}
	client, ok := endpoint.Provider.(InteractionResourceClient)
	if !ok {
		return openai.InteractionResponse{}, ErrResponseDeploymentChanged
	}
	result, err := callResponseLifecycle(r, ctx, endpoint, "interactions.cancel", func(callCtx context.Context) (openai.InteractionResponse, error) {
		return client.CancelInteraction(callCtx, id)
	})
	if err == nil {
		setInteractionBinding(&result, binding)
	}
	return result, err
}

func (r Router) DeleteInteraction(ctx context.Context, req modules.RequestContext, id string) error {
	binding, endpoint, err := r.interactionResource(ctx, req, id)
	if err != nil {
		return err
	}
	client, ok := endpoint.Provider.(InteractionResourceClient)
	if !ok {
		return ErrResponseDeploymentChanged
	}
	_, err = callResponseLifecycle(r, ctx, endpoint, "interactions.delete", func(callCtx context.Context) (struct{}, error) {
		return struct{}{}, client.DeleteInteraction(callCtx, id)
	})
	if err != nil {
		var providerErr *Error
		if !errors.As(err, &providerErr) || providerErr.StatusCode != http.StatusNotFound {
			return err
		}
	}
	return r.ownership.remove(ctx, req, id, binding)
}

func requiredInteractionCapabilities(request openai.InteractionRequest) []string {
	required := []string{"interactions"}
	if interactionUsesAgent(request) {
		required = append(required, "interaction_agents")
	}
	if request.Stream {
		required = append(required, "stream")
	}
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

func interactionRoutingModel(request openai.InteractionRequest) string {
	if strings.TrimSpace(request.Model) != "" {
		return request.Model
	}
	return strings.TrimSpace(request.Agent)
}

func interactionUsesAgent(request openai.InteractionRequest) bool {
	return strings.TrimSpace(request.Agent) != ""
}

func setInteractionBinding(result *openai.InteractionResponse, binding responseOwnership) {
	if binding.Agent {
		result.Agent, result.Model = binding.Model, ""
		return
	}
	result.Model = binding.Model
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
