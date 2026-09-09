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

func (r Router) TranscribeAudio(ctx context.Context, req modules.RequestContext) (openai.AudioTranscriptionResponse, error) {
	if req.AudioTranscriptionRequest == nil {
		return openai.AudioTranscriptionResponse{}, errors.New("missing audio transcription request")
	}
	request := *req.AudioTranscriptionRequest
	if message := request.Validate(); message != "" {
		return openai.AudioTranscriptionResponse{}, &Error{Class: FailureClientRequest, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	candidates := r.routeCandidates(ctx, req, openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model}, "audio_transcription")
	if len(candidates) == 0 {
		return openai.AudioTranscriptionResponse{}, fmt.Errorf("no audio transcription endpoint for provider=%q model=%q", request.Provider, request.Model)
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
		client, ok := endpoint.Provider.(AudioTranscriptionClient)
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
				return openai.AudioTranscriptionResponse{}, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			errs = append(errs, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			continue
		}
		if attemptCtx.AudioTranscriptionRequest == nil {
			return openai.AudioTranscriptionResponse{}, fmt.Errorf("%s/%s modules removed audio transcription request", endpoint.Type, endpoint.Name)
		}
		started := time.Now()
		lastAttempt = &attemptCtx
		response, retries, err := r.callAudioTranscription(ctx, endpoint, client, *attemptCtx.AudioTranscriptionRequest)
		totalRetries += retries
		setAttemptMetadata(&attemptCtx, started, err)
		setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
		if err == nil {
			if message := response.Validate(); message != "" {
				err = errors.New(message)
			} else {
				attemptCtx.AudioTranscriptionResponse = &response
				if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
					return openai.AudioTranscriptionResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
				}
				return response, nil
			}
		}
		errs = append(errs, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err))
		progress.fail(err)
		if ctx.Err() != nil || !progress.hasNext(candidates[candidateIndex+1:]) {
			joined := errors.Join(errs...)
			r.modules.RunFailure(ctx, lastAttempt, joined)
			return openai.AudioTranscriptionResponse{}, joined
		}
		fallbackCount++
	}
	if len(errs) == 0 {
		errs = append(errs, errors.New("no selected endpoint implements audio transcription"))
	}
	joined := errors.Join(errs...)
	if lastAttempt != nil {
		r.modules.RunFailure(ctx, lastAttempt, joined)
	}
	return openai.AudioTranscriptionResponse{}, joined
}

func (r Router) callAudioTranscription(ctx context.Context, endpoint Endpoint, client AudioTranscriptionClient, request openai.AudioTranscriptionRequest) (openai.AudioTranscriptionResponse, int, error) {
	release, err := endpoint.Admission.acquire(ctx, endpoint.Name)
	if err != nil {
		return openai.AudioTranscriptionResponse{}, 0, err
	}
	defer release()
	if err := r.health.permit(ctx, endpoint); err != nil {
		return openai.AudioTranscriptionResponse{}, 0, err
	}
	for attempt := 0; attempt <= endpointMaxRetries(endpoint); attempt++ {
		providerCtx, finish := r.startProviderCall(ctx, endpoint, "audio_transcription")
		response, callErr := client.TranscribeAudio(providerCtx, request)
		finish(callErr)
		if callErr == nil {
			r.health.success(ctx, endpoint)
			return response, attempt, nil
		}
		if ctx.Err() != nil || attempt >= endpointRetryLimit(endpoint, callErr) || !retrySameEndpointWithPolicy(endpoint, callErr) {
			r.health.failure(ctx, endpoint, callErr)
			return openai.AudioTranscriptionResponse{}, attempt, callErr
		}
		if waitErr := r.retry.beforeRetry(ctx, callErr, attempt); waitErr != nil {
			r.health.failure(ctx, endpoint, waitErr)
			return openai.AudioTranscriptionResponse{}, attempt, waitErr
		}
	}
	return openai.AudioTranscriptionResponse{}, endpointMaxRetries(endpoint), errors.New("audio transcription failed")
}
