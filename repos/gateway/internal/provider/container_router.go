package provider

import (
	"context"
	"errors"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

var ErrContainerDeploymentChanged = errors.New("container deployment changed")

func (r Router) CreateContainer(ctx context.Context, identity modules.RequestContext, input openai.ContainerCreateRequest, admit func(context.Context, *modules.RequestContext) error) (openai.Container, ContainerBinding, error) {
	for _, endpoint := range r.candidates(ctx, openai.ChatCompletionRequest{Model: input.Model, Provider: input.Provider}, "container") {
		client, ok := endpoint.Provider.(ContainerClient)
		if !ok {
			continue
		}
		release, err := r.acquireEndpoint(ctx, endpoint, 0)
		if err != nil {
			continue
		}
		if err = r.health.permit(ctx, endpoint); err != nil {
			release()
			continue
		}
		attempt := providerAttemptContext(identity, endpoint)
		attempt.Request.Model = input.Model
		attempt.Metadata["gateway.api_type"] = "container"
		r.applyCatalogPricing(ctx, &attempt, endpoint, input.Model)
		if admit != nil {
			if err = admit(ctx, &attempt); err != nil {
				release()
				return openai.Container{}, ContainerBinding{}, err
			}
		}
		providerCtx, finish := r.startProviderCall(ctx, endpoint, "container.create")
		container, callErr := client.CreateContainer(providerCtx, openai.ContainerProviderCreateRequest{Name: input.Name, ExpiresAfter: input.ExpiresAfter, MemoryLimit: input.MemoryLimit})
		finish(callErr)
		release()
		if callErr != nil {
			r.health.failure(ctx, endpoint, callErr)
			return openai.Container{}, ContainerBinding{}, callErr
		}
		r.health.success(ctx, endpoint)
		return container, ContainerBinding{Endpoint: endpoint.Name, Model: input.Model, Deployment: responseDeploymentIdentity(endpoint)}, nil
	}
	return openai.Container{}, ContainerBinding{}, errors.New("no eligible container deployment")
}

func (r Router) containerClient(binding ContainerBinding) (Endpoint, ContainerClient, error) {
	for _, endpoint := range r.runtimeEndpoints() {
		if endpoint.Name == binding.Endpoint && binding.Model != "" && binding.Deployment == responseDeploymentIdentity(endpoint) && endpoint.supportsModel(binding.Model) && endpoint.supportsCapabilities("container") {
			if client, ok := endpoint.Provider.(ContainerClient); ok {
				return endpoint, client, nil
			}
		}
	}
	return Endpoint{}, nil, ErrContainerDeploymentChanged
}

func (r Router) RetrieveContainer(ctx context.Context, binding ContainerBinding, id string) (openai.Container, error) {
	return callContainerLifecycle(r, ctx, binding, "container.retrieve", func(ctx context.Context, client ContainerClient) (openai.Container, error) {
		return client.RetrieveContainer(ctx, id)
	})
}

func (r Router) DeleteContainer(ctx context.Context, binding ContainerBinding, id string) (openai.ContainerDeletion, error) {
	return callContainerLifecycle(r, ctx, binding, "container.delete", func(ctx context.Context, client ContainerClient) (openai.ContainerDeletion, error) {
		return client.DeleteContainer(ctx, id)
	})
}

func callContainerLifecycle[T any](r Router, ctx context.Context, binding ContainerBinding, operation string, call func(context.Context, ContainerClient) (T, error)) (T, error) {
	var zero T
	endpoint, client, err := r.containerClient(binding)
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
