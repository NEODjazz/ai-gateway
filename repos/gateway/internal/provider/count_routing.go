package provider

import (
	"context"
	"fmt"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type TokenCountProvider interface {
	CountTokens(context.Context, modules.RequestContext) (TokenCountResult, error)
}

func (r Router) CountTokens(ctx context.Context, req modules.RequestContext) (TokenCountResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	candidates := r.routeCandidates(ctx, req, req.Request, requiredChatCapabilities(req.Request, false)...)
	if len(candidates) == 0 {
		return TokenCountResult{}, fmt.Errorf("no token-count endpoint for model %q", req.Request.Model)
	}
	progress := newRouteProgress(candidates)
	for _, endpoint := range candidates {
		if !progress.allows(endpoint) {
			continue
		}
		counter, ok := endpoint.Provider.(TokenCountClient)
		if !ok {
			return TokenCountResult{}, rejectParameters("provider", parameterCheck{"count_tokens", true})
		}
		if endpoint.GuardrailPolicy != "" && !endpoint.GuardrailPolicyValid {
			return TokenCountResult{}, modules.ErrGuardrailUnavailable
		}
		attempt := providerAttemptContext(req, endpoint)
		if err := validateTokenCountRequest(attempt.Request); err != nil {
			return TokenCountResult{}, err
		}
		if err := validateChatAdapter(endpoint.Provider, attempt.Request); err != nil {
			return TokenCountResult{}, err
		}
		if err := r.modules.RunTokenCount(ctx, &attempt); err != nil {
			return TokenCountResult{}, err
		}
		release, err := r.acquireEndpoint(ctx, endpoint, openai.ChatInputTokens(attempt.Request))
		if err != nil {
			return TokenCountResult{}, err
		}
		started := time.Now()
		result, err := counter.CountTokens(ctx, TokenCountRequest{Model: attempt.Request.Model, Messages: attempt.Request.Messages, Tools: attempt.Request.Tools, ToolChoice: attempt.Request.ToolChoice, ParallelToolCalls: attempt.Request.ParallelToolCalls, ChatGenerationOptions: attempt.Request.ChatGenerationOptions, ResponseFormat: attempt.Request.ResponseFormat, AnthropicSkills: attempt.Request.AnthropicSkills, AnthropicContainerID: attempt.Request.AnthropicContainerID, AnthropicCodeExecution: attempt.Request.AnthropicCodeExecution, AnthropicCodeExecutionType: attempt.Request.AnthropicCodeExecutionType, AnthropicToolSearch: attempt.Request.AnthropicToolSearch, AnthropicClientTools: attempt.Request.AnthropicClientTools, AnthropicClientToolsets: attempt.Request.AnthropicClientToolsets})
		release()
		if r.observer != nil {
			outcome := "ok"
			if err != nil {
				outcome = "error"
			}
			r.observer.ObserveProvider(endpoint.Name, endpoint.Type, "count_tokens", outcome, time.Since(started))
		}
		return result, err
	}
	if progress.initialFailure != nil {
		return TokenCountResult{}, progress.initialFailure
	}
	return TokenCountResult{}, fmt.Errorf("no eligible token-count route")
}
