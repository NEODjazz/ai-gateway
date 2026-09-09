package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

var ErrResponseCompactionUnsupported = errors.New("response compaction is not supported by the selected deployment")
var ErrResponseCompactionAnonymized = errors.New("response compaction cannot preserve anonymized conversation state")

func decodeCompactedResponse(reader io.Reader) (openai.CompactedResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxResponseJSONBytes+1))
	if err != nil {
		return openai.CompactedResponse{}, err
	}
	if len(payload) > maxResponseJSONBytes {
		return openai.CompactedResponse{}, errors.New("compacted response exceeds 32 MiB")
	}
	var response openai.CompactedResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return openai.CompactedResponse{}, err
	}
	if err := validateCompactedResponse(response); err != nil {
		return openai.CompactedResponse{}, err
	}
	return response, nil
}

func validateCompactedResponse(response openai.CompactedResponse) error {
	if response.ID == "" || len(response.ID) > 512 {
		return errors.New("provider returned invalid compacted response ID")
	}
	if response.Object != "response.compaction" || response.CreatedAt < 0 {
		return errors.New("provider returned invalid compacted response object")
	}
	if len(response.Output) == 0 || len(response.Output) > 10000 {
		return errors.New("provider returned invalid compacted response output")
	}
	for index, item := range response.Output {
		var object map[string]json.RawMessage
		if len(item) == 0 || json.Unmarshal(item, &object) != nil || object == nil {
			return errors.New("provider returned invalid compacted response item")
		}
		if index == len(response.Output)-1 {
			var itemType string
			if json.Unmarshal(object["type"], &itemType) != nil || itemType != "compaction" {
				return errors.New("provider returned compacted output without a final compaction item")
			}
		}
	}
	return validateResponseUsage(response.Usage)
}

func (r Router) CompactResponse(ctx context.Context, req modules.RequestContext) (openai.CompactedResponse, error) {
	if req.ResponseRequest == nil {
		return openai.CompactedResponse{}, errors.New("missing response compaction request")
	}
	request := *req.ResponseRequest
	candidates, affinityErr := r.responseCandidates(ctx, req, request, "responses")
	if affinityErr != nil {
		return openai.CompactedResponse{}, affinityErr
	}
	if len(candidates) == 0 {
		return openai.CompactedResponse{}, fmt.Errorf("no response endpoint for provider=%q model=%q", request.Provider, request.Model)
	}
	implementedCandidates := candidates[:0]
	for _, endpoint := range candidates {
		if _, ok := endpoint.Provider.(ResponseCompactClient); ok {
			implementedCandidates = append(implementedCandidates, endpoint)
		}
	}
	candidates = implementedCandidates
	if len(candidates) == 0 {
		return openai.CompactedResponse{}, ErrResponseCompactionUnsupported
	}

	var errs []error
	var lastAttempt *modules.RequestContext
	totalRetries := 0
	fallbackCount := 0
	progress := newRouteProgress(candidates)
	if progress.initialFailure != nil {
		errs = append(errs, progress.initialFailure)
		fallbackCount = 1
	}
	for candidateIndex, endpoint := range candidates {
		client := endpoint.Provider.(ResponseCompactClient)
		if !progress.allows(endpoint) {
			continue
		}
		progress.enter(endpoint)
		attemptCtx := providerAttemptContext(req, endpoint)
		r.applyCatalogPricing(ctx, &attemptCtx, endpoint, request.Model)
		if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
			err := fmt.Errorf("%s/%s has unknown guardrail policy %q", endpoint.Type, endpoint.Name, endpoint.GuardrailPolicy)
			errs = append(errs, err)
			progress.fail(err)
			continue
		}
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			if terminalModuleError(err) || ctx.Err() != nil {
				return openai.CompactedResponse{}, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			wrapped := fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			errs = append(errs, wrapped)
			progress.fail(err)
			continue
		}
		if attemptCtx.ResponseRequest == nil {
			return openai.CompactedResponse{}, errors.New("module removed response compaction request")
		}
		if len(attemptCtx.AnonymizationValues) > 0 {
			r.modules.RunFailure(ctx, &attemptCtx, ErrResponseCompactionAnonymized)
			return openai.CompactedResponse{}, ErrResponseCompactionAnonymized
		}
		effective := attemptCtx.ResponseRequest
		compactRequest := openai.ResponseCompactRequest{
			Provider: effective.Provider, Model: effective.Model, Input: effective.Input, Instructions: effective.Instructions,
		}
		started := time.Now()
		lastAttempt = &attemptCtx
		response, retries, err := r.callResponseCompact(ctx, endpoint, client, compactRequest)
		totalRetries += retries
		setAttemptMetadata(&attemptCtx, started, err)
		setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
		if err == nil {
			attemptCtx.CompactedResponse = &response
			if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
				return openai.CompactedResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
			}
			return response, nil
		}
		errs = append(errs, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err))
		progress.fail(err)
		if ctx.Err() != nil || !progress.hasNext(candidates[candidateIndex+1:]) {
			joined := errors.Join(errs...)
			r.modules.RunFailure(ctx, lastAttempt, joined)
			return openai.CompactedResponse{}, joined
		}
		fallbackCount++
	}
	joined := errors.Join(errs...)
	if lastAttempt != nil {
		r.modules.RunFailure(ctx, lastAttempt, joined)
	}
	return openai.CompactedResponse{}, joined
}

func (r Router) callResponseCompact(ctx context.Context, endpoint Endpoint, client ResponseCompactClient, request openai.ResponseCompactRequest) (openai.CompactedResponse, int, error) {
	release, err := r.acquireEndpoint(ctx, endpoint, openai.ReserveTokens(openai.ResponseCompactInputTokens(request), 0))
	if err != nil {
		return openai.CompactedResponse{}, 0, err
	}
	defer release()
	if err := r.health.permit(ctx, endpoint); err != nil {
		return openai.CompactedResponse{}, 0, err
	}
	for attempt := 0; attempt <= endpointMaxRetries(endpoint); attempt++ {
		providerCtx, finishProviderCall := r.startProviderCall(ctx, endpoint, "responses_compact")
		response, callErr := client.CompactResponse(providerCtx, request)
		if callErr == nil {
			callErr = validateCompactedResponse(response)
		}
		finishProviderCall(callErr)
		if callErr == nil {
			r.health.success(ctx, endpoint)
			return response, attempt, nil
		}
		if ctx.Err() != nil || attempt >= endpointRetryLimit(endpoint, callErr) || !retrySameEndpointWithPolicy(endpoint, callErr) {
			r.health.failure(ctx, endpoint, callErr)
			return openai.CompactedResponse{}, attempt, callErr
		}
		if waitErr := r.retry.beforeRetry(ctx, callErr, attempt); waitErr != nil {
			r.health.failure(ctx, endpoint, waitErr)
			return openai.CompactedResponse{}, attempt, waitErr
		}
	}
	return openai.CompactedResponse{}, endpointMaxRetries(endpoint), errors.New("response compaction retry loop ended unexpectedly")
}
