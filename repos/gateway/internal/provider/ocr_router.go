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

func (r Router) OCR(ctx context.Context, req modules.RequestContext) (openai.OCRResponse, error) {
	if req.OCRRequest == nil {
		return openai.OCRResponse{}, errors.New("missing OCR request")
	}
	request := *req.OCRRequest
	if message := request.Validate(); message != "" {
		return openai.OCRResponse{}, &Error{Class: FailureClientRequest, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	candidates := r.routeCandidates(ctx, req, openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model}, "ocr")
	if len(candidates) == 0 {
		return openai.OCRResponse{}, fmt.Errorf("no OCR endpoint for provider=%q model=%q", request.Provider, request.Model)
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
		client, ok := endpoint.Provider.(OCRClient)
		if !ok || (endpoint.Type != "mistral" && endpoint.Type != "gemini") {
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
				return openai.OCRResponse{}, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			errs = append(errs, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			continue
		}
		if attemptCtx.OCRRequest == nil {
			return openai.OCRResponse{}, fmt.Errorf("%s/%s modules removed OCR request", endpoint.Type, endpoint.Name)
		}
		request = *attemptCtx.OCRRequest
		if message := request.Validate(); message != "" {
			return openai.OCRResponse{}, fmt.Errorf("%s/%s modules returned invalid OCR request", endpoint.Type, endpoint.Name)
		}
		attemptCtx.OCRRequest = &request
		attemptCtx.InputPages = request.ReservePages()
		started := time.Now()
		lastAttempt = &attemptCtx
		response, retries, callErr := r.callOCR(ctx, endpoint, client, request)
		if callErr == nil {
			callErr = validateOCRResponse(response)
		}
		totalRetries += retries
		setAttemptMetadata(&attemptCtx, started, callErr)
		setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
		if callErr == nil {
			attemptCtx.InputPages = response.UsageInfo.PagesProcessed
			attemptCtx.OCRResponse = &response
			if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
				return openai.OCRResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
			}
			return response, nil
		}
		errs = append(errs, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, callErr))
		progress.fail(callErr)
		if ctx.Err() != nil || !progress.hasNext(candidates[candidateIndex+1:]) {
			joined := errors.Join(errs...)
			r.modules.RunFailure(ctx, lastAttempt, joined)
			return openai.OCRResponse{}, joined
		}
		fallbackCount++
	}
	if len(errs) == 0 {
		errs = append(errs, errors.New("no selected endpoint implements OCR"))
	}
	joined := errors.Join(errs...)
	if lastAttempt != nil {
		r.modules.RunFailure(ctx, lastAttempt, joined)
	}
	return openai.OCRResponse{}, joined
}

func (r Router) callOCR(ctx context.Context, endpoint Endpoint, client OCRClient, request openai.OCRRequest) (openai.OCRResponse, int, error) {
	release, err := r.acquireEndpoint(ctx, endpoint, request.InputTokens())
	if err != nil {
		return openai.OCRResponse{}, 0, err
	}
	defer release()
	if err := r.health.permit(ctx, endpoint); err != nil {
		return openai.OCRResponse{}, 0, err
	}
	for attempt := 0; attempt <= endpointMaxRetries(endpoint); attempt++ {
		providerCtx, finish := r.startProviderCall(ctx, endpoint, "ocr")
		response, callErr := client.OCR(providerCtx, request)
		finish(callErr)
		if callErr == nil {
			r.health.success(ctx, endpoint)
			return response, attempt, nil
		}
		if ctx.Err() != nil || attempt >= endpointRetryLimit(endpoint, callErr) || !retrySameEndpointWithPolicy(endpoint, callErr) {
			r.health.failure(ctx, endpoint, callErr)
			return openai.OCRResponse{}, attempt, callErr
		}
		if waitErr := r.retry.beforeRetry(ctx, callErr, attempt); waitErr != nil {
			r.health.failure(ctx, endpoint, waitErr)
			return openai.OCRResponse{}, attempt, waitErr
		}
	}
	return openai.OCRResponse{}, endpointMaxRetries(endpoint), errors.New("OCR failed")
}
