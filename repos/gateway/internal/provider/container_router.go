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

func (r Router) containerFileClient(binding ContainerBinding) (Endpoint, ContainerFileClient, error) {
	endpoint, _, err := r.containerClient(binding)
	if err != nil || !endpoint.supportsCapabilities("container", "container_files") {
		return Endpoint{}, nil, ErrContainerDeploymentChanged
	}
	client, ok := endpoint.Provider.(ContainerFileClient)
	if !ok {
		return Endpoint{}, nil, ErrContainerDeploymentChanged
	}
	return endpoint, client, nil
}

func (r Router) CreateContainerFile(ctx context.Context, binding ContainerBinding, containerID string, upload ContainerFileUpload) (openai.ContainerFile, error) {
	return callContainerFileLifecycle(r, ctx, binding, "container.file.create", func(ctx context.Context, client ContainerFileClient) (openai.ContainerFile, error) {
		return client.CreateContainerFile(ctx, containerID, upload)
	})
}

func (r Router) ListContainerFiles(ctx context.Context, binding ContainerBinding, containerID string, options ContainerFileListOptions) (openai.ContainerFileList, error) {
	return callContainerFileLifecycle(r, ctx, binding, "container.file.list", func(ctx context.Context, client ContainerFileClient) (openai.ContainerFileList, error) {
		return client.ListContainerFiles(ctx, containerID, options)
	})
}

func (r Router) RetrieveContainerFile(ctx context.Context, binding ContainerBinding, containerID, fileID string) (openai.ContainerFile, error) {
	return callContainerFileLifecycle(r, ctx, binding, "container.file.retrieve", func(ctx context.Context, client ContainerFileClient) (openai.ContainerFile, error) {
		return client.RetrieveContainerFile(ctx, containerID, fileID)
	})
}

func (r Router) DeleteContainerFile(ctx context.Context, binding ContainerBinding, containerID, fileID string) (openai.ContainerDeletion, error) {
	return callContainerFileLifecycle(r, ctx, binding, "container.file.delete", func(ctx context.Context, client ContainerFileClient) (openai.ContainerDeletion, error) {
		return client.DeleteContainerFile(ctx, containerID, fileID)
	})
}

func (r Router) DownloadContainerFile(ctx context.Context, binding ContainerBinding, containerID, fileID string) (ContainerFileContent, error) {
	return callContainerFileLifecycle(r, ctx, binding, "container.file.content", func(ctx context.Context, client ContainerFileClient) (ContainerFileContent, error) {
		return client.DownloadContainerFile(ctx, containerID, fileID)
	})
}

func callContainerFileLifecycle[T any](r Router, ctx context.Context, binding ContainerBinding, operation string, call func(context.Context, ContainerFileClient) (T, error)) (T, error) {
	var zero T
	endpoint, client, err := r.containerFileClient(binding)
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
