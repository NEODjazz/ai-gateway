package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

var ErrCompletionsUnsupported = errors.New("text completions are not supported by the selected deployment")

func decodeCompletionResponse(reader io.Reader) (openai.CompletionResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxResponseJSONBytes+1))
	if err != nil {
		return openai.CompletionResponse{}, err
	}
	if len(payload) > maxResponseJSONBytes {
		return openai.CompletionResponse{}, errors.New("completion response exceeds 32 MiB")
	}
	var response *openai.CompletionResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return openai.CompletionResponse{}, err
	}
	if response == nil {
		return openai.CompletionResponse{}, errors.New("completion response must be an object")
	}
	if err := validateCompletionResponse(*response); err != nil {
		return openai.CompletionResponse{}, err
	}
	return *response, nil
}

func validateCompletionResponse(response openai.CompletionResponse) error {
	if response.ID == "" || len(response.ID) > 512 || response.Object != "text_completion" || response.Model == "" || response.Created < 0 {
		return errors.New("provider returned invalid completion identity")
	}
	if len(response.Choices) == 0 || len(response.Choices) > 128 {
		return errors.New("provider returned invalid completion choices")
	}
	seen := make(map[int]bool, len(response.Choices))
	for _, choice := range response.Choices {
		if choice.Index < 0 || choice.Index >= 128 || seen[choice.Index] || choice.FinishReason == "" {
			return errors.New("provider returned invalid completion choice")
		}
		seen[choice.Index] = true
		if err := validateCompletionLogprobs(choice.Logprobs); err != nil {
			return err
		}
	}
	return validateCompletionUsage(response.Usage)
}

func validateCompletionLogprobs(logprobs *openai.CompletionLogprobs) error {
	if logprobs == nil {
		return nil
	}
	count := len(logprobs.Tokens)
	if len(logprobs.TextOffset) != count || len(logprobs.TokenLogprobs) != count || len(logprobs.TopLogprobs) != count {
		return errors.New("provider returned inconsistent completion logprobs")
	}
	for index, probability := range logprobs.TokenLogprobs {
		if logprobs.TextOffset[index] < 0 {
			return errors.New("provider returned invalid completion text offset")
		}
		if probability != nil && (math.IsNaN(*probability) || math.IsInf(*probability, 0)) {
			return errors.New("provider returned invalid completion token logprob")
		}
		for _, candidate := range logprobs.TopLogprobs[index] {
			if math.IsNaN(candidate) || math.IsInf(candidate, 0) {
				return errors.New("provider returned invalid completion top logprob")
			}
		}
	}
	return nil
}

func validateCompletionUsage(usage openai.Usage) error {
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.TotalTokens < 0 {
		return errors.New("provider returned negative completion usage")
	}
	if usage.PromptTokens > int(^uint(0)>>1)-usage.CompletionTokens {
		return errors.New("completion usage exceeds integer range")
	}
	if usage.TotalTokens < usage.PromptTokens+usage.CompletionTokens {
		return errors.New("provider returned inconsistent completion usage")
	}
	if details := usage.PromptTokensDetails; details != nil {
		if details.CachedTokens < 0 || details.CacheWriteTokens < 0 || details.CacheCreationTokens < 0 {
			return errors.New("provider returned negative completion cache usage")
		}
	}
	if details := usage.CompletionTokensDetails; details != nil && details.ReasoningTokens < 0 {
		return errors.New("provider returned negative completion reasoning usage")
	}
	return nil
}

func validateCompletionResult(response openai.CompletionResponse, request openai.CompletionRequest) error {
	if err := validateCompletionResponse(response); err != nil {
		return err
	}
	want, err := openai.CompletionChoiceCount(request.Prompt, request.N)
	if err != nil {
		return err
	}
	if len(response.Choices) != want {
		return errors.New("provider returned an unexpected number of completion choices")
	}
	for _, choice := range response.Choices {
		if choice.Index >= want {
			return errors.New("provider returned completion choice index outside the requested range")
		}
	}
	return nil
}

func (r Router) Completions(ctx context.Context, req modules.RequestContext) (openai.CompletionResponse, error) {
	if req.CompletionRequest == nil {
		return openai.CompletionResponse{}, errors.New("missing completion request")
	}
	request := *req.CompletionRequest
	if _, err := openai.CompletionChoiceCount(request.Prompt, request.N); err != nil {
		return openai.CompletionResponse{}, err
	}
	candidates := r.routeCandidates(ctx, req, req.Request, "chat")
	if len(candidates) == 0 {
		return openai.CompletionResponse{}, fmt.Errorf("no completion endpoint for provider=%q model=%q", request.Provider, request.Model)
	}
	implemented := candidates[:0]
	for _, endpoint := range candidates {
		if _, ok := endpoint.Provider.(CompletionClient); ok {
			implemented = append(implemented, endpoint)
		}
	}
	candidates = implemented
	if len(candidates) == 0 {
		return openai.CompletionResponse{}, ErrCompletionsUnsupported
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
				return openai.CompletionResponse{}, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			wrapped := fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			errs = append(errs, wrapped)
			progress.fail(err)
			continue
		}
		if attemptCtx.CompletionRequest == nil || len(attemptCtx.Request.Messages) != 1 {
			return openai.CompletionResponse{}, errors.New("module removed completion request")
		}
		effectivePrompt, err := openai.ApplyCompletionPromptPolicyContent(attemptCtx.CompletionRequest.Prompt, attemptCtx.Request.Messages[0].Content)
		if err != nil {
			return openai.CompletionResponse{}, err
		}
		attemptCtx.CompletionRequest.Prompt = effectivePrompt
		started := time.Now()
		lastAttempt = &attemptCtx
		client := endpoint.Provider.(CompletionClient)
		response, retries, err := r.callCompletion(ctx, endpoint, client, *attemptCtx.CompletionRequest)
		totalRetries += retries
		setAttemptMetadata(&attemptCtx, started, err)
		setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
		if err == nil {
			attemptCtx.CompletionResponse = &response
			if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
				return openai.CompletionResponse{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
			}
			for index := range response.Choices {
				response.Choices[index].Text = modules.DeanonymizeText(response.Choices[index].Text, attemptCtx.AnonymizationValues)
			}
			return response, nil
		}
		errs = append(errs, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, err))
		progress.fail(err)
		if ctx.Err() != nil || !progress.hasNext(candidates[candidateIndex+1:]) {
			joined := errors.Join(errs...)
			r.modules.RunFailure(ctx, lastAttempt, joined)
			return openai.CompletionResponse{}, joined
		}
		fallbackCount++
	}
	joined := errors.Join(errs...)
	if lastAttempt != nil {
		r.modules.RunFailure(ctx, lastAttempt, joined)
	}
	return openai.CompletionResponse{}, joined
}

func (r Router) StreamCompletions(ctx context.Context, req modules.RequestContext, write CompletionStreamWriter) (openai.CompletionResponse, bool, error) {
	if req.CompletionRequest == nil {
		return openai.CompletionResponse{}, false, errors.New("missing completion request")
	}
	request := *req.CompletionRequest
	if _, err := openai.CompletionChoiceCount(request.Prompt, request.N); err != nil {
		return openai.CompletionResponse{}, false, err
	}
	candidates := r.routeCandidates(ctx, req, req.Request, "chat")
	if len(candidates) == 0 {
		return openai.CompletionResponse{}, false, nil
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
		client, ok := endpoint.Provider.(StreamingCompletionClient)
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
				return openai.CompletionResponse{}, false, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			wrapped := fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			errs = append(errs, wrapped)
			progress.fail(err)
			continue
		}
		if attemptCtx.CompletionRequest == nil || len(attemptCtx.Request.Messages) != 1 {
			return openai.CompletionResponse{}, false, errors.New("module removed completion request")
		}
		effectivePrompt, err := openai.ApplyCompletionPromptPolicyContent(attemptCtx.CompletionRequest.Prompt, attemptCtx.Request.Messages[0].Content)
		if err != nil {
			return openai.CompletionResponse{}, false, err
		}
		attemptCtx.CompletionRequest.Prompt = effectivePrompt
		attemptCtx.CompletionRequest.Stream = true
		lastAttempt = &attemptCtx
		started := time.Now()
		release, err := endpoint.Admission.acquire(ctx, endpoint.Name)
		if err != nil {
			setAttemptMetadata(&attemptCtx, started, err)
			setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
			errs = append(errs, fmt.Errorf("%s/%s admission failed: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			fallbackCount++
			continue
		}
		if err := r.health.permit(ctx, endpoint); err != nil {
			release()
			setAttemptMetadata(&attemptCtx, started, err)
			setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
			errs = append(errs, fmt.Errorf("%s/%s circuit denied call: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			fallbackCount++
			continue
		}

		var response openai.CompletionResponse
		streamStarted := false
		firstTokenLatency := time.Duration(0)
		for retry := 0; ; retry++ {
			tracker := newStreamAttemptTracker(started)
			providerCtx, finishProviderCall := r.startProviderCall(ctx, endpoint, "completions.stream")
			response, err = client.StreamCompletions(providerCtx, *attemptCtx.CompletionRequest, deanonymizingCompletionStreamWriter(attemptCtx.AnonymizationValues, tracker.completionWriter(write)))
			if err == nil {
				err = validateCompletionResult(response, *attemptCtx.CompletionRequest)
			}
			finishProviderCall(err)
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
			if streamStarted || ctx.Err() != nil || !progress.hasNext(candidates[candidateIndex+1:]) {
				r.modules.RunFailure(ctx, &attemptCtx, wrapped)
				return openai.CompletionResponse{}, streamStarted, wrapped
			}
			errs = append(errs, wrapped)
			fallbackCount++
			continue
		}
		r.health.success(ctx, endpoint)
		attemptCtx.CompletionResponse = &response
		if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
			return openai.CompletionResponse{}, true, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
		}
		for index := range response.Choices {
			response.Choices[index].Text = modules.DeanonymizeText(response.Choices[index].Text, attemptCtx.AnonymizationValues)
		}
		return response, true, nil
	}
	if len(errs) > 0 {
		joined := errors.Join(errs...)
		if lastAttempt != nil {
			r.modules.RunFailure(ctx, lastAttempt, joined)
		}
		return openai.CompletionResponse{}, false, joined
	}
	return openai.CompletionResponse{}, false, nil
}

func (r Router) callCompletion(ctx context.Context, endpoint Endpoint, client CompletionClient, request openai.CompletionRequest) (openai.CompletionResponse, int, error) {
	release, err := endpoint.Admission.acquire(ctx, endpoint.Name)
	if err != nil {
		return openai.CompletionResponse{}, 0, err
	}
	defer release()
	if err := r.health.permit(ctx, endpoint); err != nil {
		return openai.CompletionResponse{}, 0, err
	}
	for attempt := 0; attempt <= endpointMaxRetries(endpoint); attempt++ {
		providerCtx, finishProviderCall := r.startProviderCall(ctx, endpoint, "completions")
		response, callErr := client.Completions(providerCtx, request)
		if callErr == nil {
			callErr = validateCompletionResult(response, request)
		}
		finishProviderCall(callErr)
		if callErr == nil {
			r.health.success(ctx, endpoint)
			return response, attempt, nil
		}
		if ctx.Err() != nil || attempt >= endpointRetryLimit(endpoint, callErr) || !retrySameEndpointWithPolicy(endpoint, callErr) {
			r.health.failure(ctx, endpoint, callErr)
			return openai.CompletionResponse{}, attempt, callErr
		}
		if waitErr := r.retry.beforeRetry(ctx, callErr, attempt); waitErr != nil {
			r.health.failure(ctx, endpoint, waitErr)
			return openai.CompletionResponse{}, attempt, waitErr
		}
	}
	return openai.CompletionResponse{}, endpointMaxRetries(endpoint), errors.New("completion retry loop ended unexpectedly")
}
