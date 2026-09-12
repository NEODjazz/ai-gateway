package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func (r Router) ExecuteSandbox(ctx context.Context, req modules.RequestContext) (openai.SandboxExecutionResult, error) {
	if req.SandboxRequest == nil {
		return openai.SandboxExecutionResult{}, errors.New("missing sandbox request")
	}
	request := *req.SandboxRequest
	if message := request.Validate(); message != "" {
		return openai.SandboxExecutionResult{}, &Error{Class: FailureClientRequest, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Err: errors.New(message)}
	}
	candidates := r.routeCandidates(ctx, req, openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model}, "sandbox")
	if len(candidates) == 0 {
		return openai.SandboxExecutionResult{}, fmt.Errorf("no sandbox endpoint for provider=%q model=%q", request.Provider, request.Model)
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
		client, ok := endpoint.Provider.(SandboxClient)
		if !ok || endpoint.Type != "opensandbox" {
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
				return openai.SandboxExecutionResult{}, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err)
			}
			errs = append(errs, fmt.Errorf("%s/%s modules failed: %w", endpoint.Type, endpoint.Name, err))
			progress.fail(err)
			continue
		}
		if attemptCtx.SandboxRequest == nil || len(attemptCtx.Request.Messages) != 1 {
			return openai.SandboxExecutionResult{}, fmt.Errorf("%s/%s modules removed sandbox request", endpoint.Type, endpoint.Name)
		}
		request = *attemptCtx.SandboxRequest
		request.Code = openai.ContentText(attemptCtx.Request.Messages[0].Content)
		if message := request.Validate(); message != "" {
			return openai.SandboxExecutionResult{}, fmt.Errorf("%s/%s modules returned invalid sandbox request", endpoint.Type, endpoint.Name)
		}
		attemptCtx.SandboxRequest = &request
		attemptCtx.InputCharacters = len(request.Code)
		started := time.Now()
		lastAttempt = &attemptCtx
		response, retries, callErr := r.callSandbox(ctx, endpoint, client, request)
		if callErr == nil {
			callErr = validateSandboxExecutionResult(response)
		}
		totalRetries += retries
		setAttemptMetadata(&attemptCtx, started, callErr)
		setAttemptCounters(&attemptCtx, totalRetries, fallbackCount)
		if callErr == nil {
			payload, encodeErr := json.Marshal(response)
			if encodeErr != nil {
				return openai.SandboxExecutionResult{}, encodeErr
			}
			attemptCtx.Response = &openai.ChatCompletionResponse{Model: request.Model, Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", Content: string(payload)}, FinishReason: "stop"}}, Usage: openai.Usage{}}
			attemptCtx.Usage = &attemptCtx.Response.Usage
			if err := r.modules.RunPostResponse(ctx, &attemptCtx); err != nil {
				return openai.SandboxExecutionResult{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: err}
			}
			if attemptCtx.Response == nil || len(attemptCtx.Response.Choices) != 1 || json.Unmarshal([]byte(openai.ContentText(attemptCtx.Response.Choices[0].Message.Content)), &response) != nil || validateSandboxExecutionResult(response) != nil {
				return openai.SandboxExecutionResult{}, &Error{Class: FailurePostProcessing, Provider: endpoint.Name, Err: errors.New("post-response module returned an invalid sandbox result")}
			}
			return response, nil
		}
		errs = append(errs, fmt.Errorf("%s/%s failed: %w", endpoint.Type, endpoint.Name, callErr))
		progress.fail(callErr)
		if ctx.Err() != nil || !progress.hasNext(candidates[candidateIndex+1:]) {
			joined := errors.Join(errs...)
			r.modules.RunFailure(ctx, lastAttempt, joined)
			return openai.SandboxExecutionResult{}, joined
		}
		fallbackCount++
	}
	if len(errs) == 0 {
		errs = append(errs, errors.New("no selected endpoint implements sandbox execution"))
	}
	joined := errors.Join(errs...)
	if lastAttempt != nil {
		r.modules.RunFailure(ctx, lastAttempt, joined)
	}
	return openai.SandboxExecutionResult{}, joined
}

func (r Router) callSandbox(ctx context.Context, endpoint Endpoint, client SandboxClient, request openai.SandboxExecuteRequest) (openai.SandboxExecutionResult, int, error) {
	release, err := r.acquireEndpoint(ctx, endpoint, request.InputTokens())
	if err != nil {
		return openai.SandboxExecutionResult{}, 0, err
	}
	defer release()
	if err := r.health.permit(ctx, endpoint); err != nil {
		return openai.SandboxExecutionResult{}, 0, err
	}
	for attempt := 0; attempt <= endpointMaxRetries(endpoint); attempt++ {
		providerCtx, finish := r.startProviderCall(ctx, endpoint, "sandbox.execute")
		response, callErr := client.ExecuteSandbox(providerCtx, request)
		finish(callErr)
		if callErr == nil {
			r.health.success(ctx, endpoint)
			return response, attempt, nil
		}
		if ctx.Err() != nil || attempt >= endpointRetryLimit(endpoint, callErr) || !retrySameEndpointWithPolicy(endpoint, callErr) {
			r.health.failure(ctx, endpoint, callErr)
			return openai.SandboxExecutionResult{}, attempt, callErr
		}
		if waitErr := r.retry.beforeRetry(ctx, callErr, attempt); waitErr != nil {
			r.health.failure(ctx, endpoint, waitErr)
			return openai.SandboxExecutionResult{}, attempt, waitErr
		}
	}
	return openai.SandboxExecutionResult{}, endpointMaxRetries(endpoint), errors.New("sandbox execution failed")
}
