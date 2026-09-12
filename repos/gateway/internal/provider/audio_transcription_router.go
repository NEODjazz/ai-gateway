package provider

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func (r Router) TranscribeAudio(ctx context.Context, req modules.RequestContext) (openai.AudioTranscriptionResponse, error) {
	return r.routeAudio(ctx, req, false)
}

func (r Router) TranslateAudio(ctx context.Context, req modules.RequestContext) (openai.AudioTranscriptionResponse, error) {
	return r.routeAudio(ctx, req, true)
}

type audioOperationCall func(context.Context, openai.AudioTranscriptionRequest) (openai.AudioTranscriptionResponse, error)

func (r Router) routeAudio(ctx context.Context, req modules.RequestContext, translation bool) (openai.AudioTranscriptionResponse, error) {
	if req.AudioTranscriptionRequest == nil {
		return openai.AudioTranscriptionResponse{}, errors.New("missing audio request")
	}
	request := *req.AudioTranscriptionRequest
	if message := request.Validate(); message != "" {
		return openai.AudioTranscriptionResponse{}, &Error{Class: FailureClientRequest, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	if request.Stream {
		return openai.AudioTranscriptionResponse{}, &Error{Class: FailureClientRequest, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New("streaming audio transcription requires StreamTranscribeAudio")}
	}
	operation := "audio_transcription"
	if translation {
		operation = "audio_translation"
	}
	candidates := r.routeCandidates(ctx, req, openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model}, operation)
	if len(candidates) == 0 {
		return openai.AudioTranscriptionResponse{}, fmt.Errorf("no %s endpoint for provider=%q model=%q", operation, request.Provider, request.Model)
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
		var call audioOperationCall
		if translation {
			client, ok := endpoint.Provider.(AudioTranslationClient)
			if !ok {
				continue
			}
			call = client.TranslateAudio
		} else {
			client, ok := endpoint.Provider.(AudioTranscriptionClient)
			if !ok {
				continue
			}
			call = client.TranscribeAudio
		}
		progress.enter(endpoint)
		attemptCtx := providerAttemptContext(req, endpoint)
		r.applyCatalogPricing(ctx, &attemptCtx, endpoint, request.Model)
		var duration int
		var reserveErr error
		if translation {
			if reserver, ok := endpoint.Provider.(AudioTranslationDurationReserver); ok {
				duration, reserveErr = reserver.ReserveTranslationAudioMilliseconds(request)
			}
		} else if reserver, ok := endpoint.Provider.(AudioTranscriptionDurationReserver); ok {
			duration, reserveErr = reserver.ReserveAudioMilliseconds(request)
		}
		if duration > 0 || reserveErr != nil {
			if reserveErr != nil {
				return openai.AudioTranscriptionResponse{}, &Error{Class: FailureClientRequest, Provider: endpoint.Name, StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_audio", Err: reserveErr}
			}
			attemptCtx.InputAudioMilliseconds = duration
		}
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
		response, retries, err := r.callAudioOperation(ctx, endpoint, call, *attemptCtx.AudioTranscriptionRequest, operation)
		totalRetries += retries
		setAttemptMetadata(&attemptCtx, started, err)
		setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
		if err == nil {
			if message := response.Validate(); message != "" {
				err = errors.New(message)
			} else {
				attemptCtx.AudioTranscriptionResponse = &response
				if response.Usage != nil && response.Usage.Type == "duration" {
					attemptCtx.InputAudioMilliseconds = response.Usage.InputAudioMilliseconds
				} else if response.Duration > 0 {
					attemptCtx.InputAudioMilliseconds = int(math.Ceil(response.Duration * 1000))
				}
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

func (r Router) StreamTranscribeAudio(ctx context.Context, req modules.RequestContext, write AudioTranscriptionStreamWriter) (openai.AudioTranscriptionResponse, bool, error) {
	if req.AudioTranscriptionRequest == nil {
		return openai.AudioTranscriptionResponse{}, true, errors.New("missing audio transcription request")
	}
	request := *req.AudioTranscriptionRequest
	request.Stream = true
	if message := request.Validate(); message != "" {
		return openai.AudioTranscriptionResponse{}, true, &Error{Class: FailureClientRequest, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	candidates := r.routeCandidates(ctx, req, openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model}, "audio_transcription")
	if len(candidates) == 0 || outputDLPRequired(req, candidates) {
		return openai.AudioTranscriptionResponse{}, false, nil
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
		client, ok := endpoint.Provider.(StreamingAudioTranscriptionClient)
		if !ok || !progress.allows(endpoint) {
			continue
		}
		progress.enter(endpoint)
		attemptCtx := providerAttemptContext(req, endpoint)
		r.applyCatalogPricing(ctx, &attemptCtx, endpoint, request.Model)
		if reserver, ok := endpoint.Provider.(AudioTranscriptionDurationReserver); ok {
			duration, err := reserver.ReserveAudioMilliseconds(request)
			if err != nil {
				return openai.AudioTranscriptionResponse{}, false, &Error{Class: FailureClientRequest, Provider: endpoint.Name, StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_audio", Err: err}
			}
			attemptCtx.InputAudioMilliseconds = duration
		}
		if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
			err := fmt.Errorf("%s/%s has unknown guardrail policy %q", endpoint.Type, endpoint.Name, endpoint.GuardrailPolicy)
			failures = append(failures, err)
			progress.fail(err)
			continue
		}
		attemptCtx.AudioTranscriptionRequest.Stream = true
		if err := r.modules.Run(ctx, &attemptCtx); err != nil {
			if terminalModuleError(err) || ctx.Err() != nil {
				return openai.AudioTranscriptionResponse{}, false, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			failures = append(failures, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			continue
		}
		if attemptCtx.AudioTranscriptionRequest == nil {
			return openai.AudioTranscriptionResponse{}, false, fmt.Errorf("%s/%s modules removed audio transcription request", endpoint.Type, endpoint.Name)
		}
		lastAttempt = &attemptCtx
		started := time.Now()
		release, err := r.acquireEndpoint(ctx, endpoint, openai.AudioTranscriptionReserveTokens(*attemptCtx.AudioTranscriptionRequest))
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
		var response openai.AudioTranscriptionResponse
		streamStarted := false
		firstTokenLatency := time.Duration(0)
		for retry := 0; ; retry++ {
			tracker := newStreamAttemptTracker(started)
			providerCtx, finish := r.startProviderCall(ctx, endpoint, "audio_transcription.stream")
			response, err = client.StreamTranscribeAudio(providerCtx, *attemptCtx.AudioTranscriptionRequest, tracker.audioTranscriptionWriter(write))
			if err == nil {
				if message := response.Validate(); message != "" {
					err = errors.New(message)
				}
			}
			finish(err)
			streamStarted, firstTokenLatency = tracker.state()
			if err == nil || errors.Is(err, ErrStreamingUnsupported) || streamStarted || ctx.Err() != nil || retry >= endpointRetryLimit(endpoint, err) || !retrySameEndpointWithPolicy(endpoint, err) {
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
		if errors.Is(err, ErrStreamingUnsupported) {
			r.health.success(ctx, endpoint)
			progress.fail(err)
			fallbackCount++
			continue
		}
		if err != nil {
			if ctx.Err() == nil {
				r.health.failure(ctx, endpoint, err)
			}
			wrapped := fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err)
			progress.fail(err)
			if streamStarted || ctx.Err() != nil || !progress.hasNext(candidates[index+1:]) {
				r.modules.RunFailure(ctx, &attemptCtx, wrapped)
				return openai.AudioTranscriptionResponse{}, streamStarted, wrapped
			}
			failures = append(failures, wrapped)
			fallbackCount++
			continue
		}
		r.health.success(ctx, endpoint)
		attemptCtx.AudioTranscriptionResponse = &response
		if response.Usage != nil && response.Usage.Type == "duration" {
			attemptCtx.InputAudioMilliseconds = response.Usage.InputAudioMilliseconds
		} else if response.Duration > 0 {
			attemptCtx.InputAudioMilliseconds = int(math.Ceil(response.Duration * 1000))
		}
		if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
			return openai.AudioTranscriptionResponse{}, true, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
		}
		return response, true, nil
	}
	if len(failures) > 0 {
		joined := errors.Join(failures...)
		if lastAttempt != nil {
			r.modules.RunFailure(ctx, lastAttempt, joined)
		}
		return openai.AudioTranscriptionResponse{}, false, joined
	}
	return openai.AudioTranscriptionResponse{}, false, nil
}

func (r Router) callAudioOperation(ctx context.Context, endpoint Endpoint, call audioOperationCall, request openai.AudioTranscriptionRequest, operation string) (openai.AudioTranscriptionResponse, int, error) {
	release, err := r.acquireEndpoint(ctx, endpoint, openai.AudioTranscriptionReserveTokens(request))
	if err != nil {
		return openai.AudioTranscriptionResponse{}, 0, err
	}
	defer release()
	if err := r.health.permit(ctx, endpoint); err != nil {
		return openai.AudioTranscriptionResponse{}, 0, err
	}
	for attempt := 0; attempt <= endpointMaxRetries(endpoint); attempt++ {
		providerCtx, finish := r.startProviderCall(ctx, endpoint, operation)
		response, callErr := call(providerCtx, request)
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
	return openai.AudioTranscriptionResponse{}, endpointMaxRetries(endpoint), errors.New("audio operation failed")
}
