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

func (r Router) GenerateSpeech(ctx context.Context, req modules.RequestContext) (openai.AudioSpeechResponse, error) {
	if req.AudioSpeechRequest == nil {
		return openai.AudioSpeechResponse{}, errors.New("missing audio speech request")
	}
	request := *req.AudioSpeechRequest
	if message := request.Validate(); message != "" {
		return openai.AudioSpeechResponse{}, &Error{Class: FailureClientRequest, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	candidates := r.routeCandidates(ctx, req, openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model}, "audio_speech")
	if len(candidates) == 0 {
		return openai.AudioSpeechResponse{}, fmt.Errorf("no audio speech endpoint for provider=%q model=%q", request.Provider, request.Model)
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
		client, ok := endpoint.Provider.(AudioSpeechClient)
		if !ok {
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
				return openai.AudioSpeechResponse{}, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			errs = append(errs, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			continue
		}
		if attemptCtx.AudioSpeechRequest == nil {
			return openai.AudioSpeechResponse{}, fmt.Errorf("%s/%s modules removed audio speech request", endpoint.Type, endpoint.Name)
		}
		if len(attemptCtx.Request.Messages) == 1 {
			attemptCtx.AudioSpeechRequest.Input = openai.ContentText(attemptCtx.Request.Messages[0].Content)
		}
		attemptCtx.InputCharacters = attemptCtx.AudioSpeechRequest.InputCharacters()
		started := time.Now()
		lastAttempt = &attemptCtx
		response, retries, err := r.callAudioSpeech(ctx, endpoint, client, *attemptCtx.AudioSpeechRequest)
		totalRetries += retries
		setAttemptMetadata(&attemptCtx, started, err)
		setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
		if err == nil {
			attemptCtx.AudioSpeechResponse = &response
			if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
				return openai.AudioSpeechResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
			}
			return response, nil
		}
		errs = append(errs, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err))
		progress.fail(err)
		if ctx.Err() != nil || !progress.hasNext(candidates[candidateIndex+1:]) {
			joined := errors.Join(errs...)
			r.modules.RunFailure(ctx, lastAttempt, joined)
			return openai.AudioSpeechResponse{}, joined
		}
		fallbackCount++
	}
	if len(errs) == 0 {
		errs = append(errs, errors.New("no selected endpoint implements audio speech"))
	}
	joined := errors.Join(errs...)
	if lastAttempt != nil {
		r.modules.RunFailure(ctx, lastAttempt, joined)
	}
	return openai.AudioSpeechResponse{}, joined
}

func (r Router) callAudioSpeech(ctx context.Context, endpoint Endpoint, client AudioSpeechClient, request openai.AudioSpeechRequest) (openai.AudioSpeechResponse, int, error) {
	release, err := r.acquireEndpoint(ctx, endpoint, openai.AudioSpeechReserveTokens(request))
	if err != nil {
		return openai.AudioSpeechResponse{}, 0, err
	}
	defer release()
	if err := r.health.permit(ctx, endpoint); err != nil {
		return openai.AudioSpeechResponse{}, 0, err
	}
	for attempt := 0; attempt <= endpointMaxRetries(endpoint); attempt++ {
		providerCtx, finish := r.startProviderCall(ctx, endpoint, "audio_speech")
		response, callErr := client.GenerateSpeech(providerCtx, request)
		finish(callErr)
		if callErr == nil {
			r.health.success(ctx, endpoint)
			return response, attempt, nil
		}
		if ctx.Err() != nil || attempt >= endpointRetryLimit(endpoint, callErr) || !retrySameEndpointWithPolicy(endpoint, callErr) {
			r.health.failure(ctx, endpoint, callErr)
			return openai.AudioSpeechResponse{}, attempt, callErr
		}
		if waitErr := r.retry.beforeRetry(ctx, callErr, attempt); waitErr != nil {
			r.health.failure(ctx, endpoint, waitErr)
			return openai.AudioSpeechResponse{}, attempt, waitErr
		}
	}
	return openai.AudioSpeechResponse{}, endpointMaxRetries(endpoint), errors.New("audio speech failed")
}
