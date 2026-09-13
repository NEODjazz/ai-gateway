package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

var ErrCachedContentDeploymentChanged = errors.New("cached content deployment changed")
var ErrCachedContentPolicyChanged = errors.New("cached content effective policy changed")

func (r Router) CreateCachedContent(ctx context.Context, identity modules.RequestContext, request openai.ChatCompletionRequest, displayName string, expiration openai.GeminiCachedContentExpiration, admit func(context.Context, *modules.RequestContext) error) (openai.GeminiCachedContent, CachedContentBinding, error) {
	for _, endpoint := range r.candidates(ctx, request, "cached_content") {
		client, ok := endpoint.Provider.(GeminiCachedContentClient)
		if !ok {
			continue
		}
		release, err := r.acquireEndpoint(ctx, endpoint, openai.ChatInputTokens(request))
		if err != nil {
			continue
		}
		if err = r.health.permit(ctx, endpoint); err != nil {
			release()
			continue
		}
		attempt := identity
		attempt.Request = request
		attempt = providerAttemptContext(attempt, endpoint)
		attempt.Metadata["gateway.api_type"] = "cached_content"
		r.applyCatalogPricing(ctx, &attempt, endpoint, request.Model)
		if admit != nil {
			if err = admit(ctx, &attempt); err != nil {
				release()
				return openai.GeminiCachedContent{}, CachedContentBinding{}, err
			}
		}
		started := time.Now()
		providerCtx, finish := r.startProviderCall(ctx, endpoint, "cached_content.create")
		content, callErr := client.CreateCachedContent(providerCtx, attempt.Request, displayName, expiration)
		finish(callErr)
		release()
		setAttemptMetadata(&attempt, started, callErr)
		if callErr != nil {
			r.health.failure(ctx, endpoint, callErr)
			r.modules.RunFailure(ctx, &attempt, callErr)
			return openai.GeminiCachedContent{}, CachedContentBinding{}, callErr
		}
		if content.UsageMetadata == nil || content.UsageMetadata.TotalTokenCount < 0 {
			err = errors.New("Gemini cached content response omitted token usage")
			r.health.failure(ctx, endpoint, err)
			if content.Name != "" {
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				if cleanupErr := client.DeleteCachedContent(cleanupCtx, content.Name); cleanupErr != nil {
					log.Printf("cached content compensation delete failed for %s: %v", content.Name, cleanupErr)
				}
				cancel()
			}
			r.modules.RunFailure(ctx, &attempt, err)
			return openai.GeminiCachedContent{}, CachedContentBinding{}, err
		}
		r.health.success(ctx, endpoint)
		tokens := content.UsageMetadata.TotalTokenCount
		attempt.Response = &openai.ChatCompletionResponse{Model: content.Model, Usage: openai.Usage{PromptTokens: tokens, TotalTokens: tokens, PromptTokensDetails: &openai.PromptTokenDetails{CacheWriteTokens: tokens}}}
		return content, CachedContentBinding{Endpoint: endpoint.Name, Model: request.Model, Deployment: responseDeploymentIdentity(endpoint), Policy: cachedContentPolicyIdentity(attempt)}, nil
	}
	return openai.GeminiCachedContent{}, CachedContentBinding{}, errors.New("no eligible cached content deployment")
}

func (r Router) cachedContentClient(binding CachedContentBinding) (Endpoint, GeminiCachedContentClient, error) {
	for _, endpoint := range r.runtimeEndpoints() {
		if endpoint.Name == binding.Endpoint && binding.Model != "" && binding.Deployment == responseDeploymentIdentity(endpoint) && endpoint.supportsModel(binding.Model) && endpoint.supportsCapabilities("cached_content") {
			if client, ok := endpoint.Provider.(GeminiCachedContentClient); ok {
				return endpoint, client, nil
			}
		}
	}
	return Endpoint{}, nil, ErrCachedContentDeploymentChanged
}

func (r Router) validateCachedContentRequestBinding(request openai.ChatCompletionRequest) error {
	if request.GeminiCachedContent == "" {
		return nil
	}
	if len(request.GeminiCachedContentPolicy) != 64 {
		return ErrCachedContentPolicyChanged
	}
	_, _, err := r.cachedContentClient(CachedContentBinding{Endpoint: request.GeminiCachedContentEndpoint, Model: request.Model, Deployment: request.GeminiCachedContentDeployment})
	return err
}

func cachedContentPolicyIdentity(request modules.RequestContext) string {
	policy := map[string]string{}
	for key, value := range request.Metadata {
		if strings.HasPrefix(key, "policy.") || strings.HasPrefix(key, "provider.modules.") || strings.HasPrefix(key, "provider.guardrail.") {
			policy[key] = value
		}
	}
	payload, _ := json.Marshal(policy)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func callCachedContentLifecycle[T any](r Router, ctx context.Context, binding CachedContentBinding, operation string, call func(context.Context, GeminiCachedContentClient) (T, error)) (T, error) {
	var zero T
	endpoint, client, err := r.cachedContentClient(binding)
	if err != nil {
		return zero, err
	}
	release, err := r.acquireEndpoint(ctx, endpoint, 0)
	if err != nil {
		return zero, err
	}
	defer release()
	if err = r.health.permit(ctx, endpoint); err != nil {
		return zero, err
	}
	providerCtx, finish := r.startProviderCall(ctx, endpoint, operation)
	result, callErr := call(providerCtx, client)
	finish(callErr)
	if callErr != nil {
		r.health.failure(ctx, endpoint, callErr)
		return zero, callErr
	}
	r.health.success(ctx, endpoint)
	return result, nil
}

func (r Router) RetrieveCachedContent(ctx context.Context, binding CachedContentBinding, name string) (openai.GeminiCachedContent, error) {
	return callCachedContentLifecycle(r, ctx, binding, "cached_content.retrieve", func(ctx context.Context, client GeminiCachedContentClient) (openai.GeminiCachedContent, error) {
		return client.GetCachedContent(ctx, name)
	})
}

func (r Router) UpdateCachedContent(ctx context.Context, binding CachedContentBinding, name string, expiration openai.GeminiCachedContentExpiration) (openai.GeminiCachedContent, error) {
	return callCachedContentLifecycle(r, ctx, binding, "cached_content.update", func(ctx context.Context, client GeminiCachedContentClient) (openai.GeminiCachedContent, error) {
		return client.UpdateCachedContent(ctx, name, expiration)
	})
}

func (r Router) DeleteCachedContent(ctx context.Context, binding CachedContentBinding, name string) error {
	_, err := callCachedContentLifecycle(r, ctx, binding, "cached_content.delete", func(ctx context.Context, client GeminiCachedContentClient) (struct{}, error) {
		return struct{}{}, client.DeleteCachedContent(ctx, name)
	})
	return err
}
