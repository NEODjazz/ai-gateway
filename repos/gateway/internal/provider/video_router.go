package provider

import (
	"context"
	"errors"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

var ErrVideoDeploymentChanged = errors.New("video deployment changed")

func (r Router) CreateVideo(ctx context.Context, identity modules.RequestContext, input openai.VideoCreateRequest, admit func(context.Context, *modules.RequestContext) error) (openai.Video, VideoBinding, error) {
	for _, endpoint := range r.candidates(ctx, openai.ChatCompletionRequest{Model: input.Model}, "video") {
		client, ok := endpoint.Provider.(VideoClient)
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
		attempt.Metadata["gateway.api_type"] = "video"
		r.applyCatalogPricing(ctx, &attempt, endpoint, input.Model)
		if admit != nil {
			if err = admit(ctx, &attempt); err != nil {
				release()
				return openai.Video{}, VideoBinding{}, err
			}
		}
		request := input
		if alias, found := endpoint.ModelAliases[input.Model]; found {
			request.Model = alias
		}
		providerCtx, finish := r.startProviderCall(ctx, endpoint, "video.create")
		video, callErr := client.CreateVideo(providerCtx, request)
		finish(callErr)
		release()
		if callErr != nil {
			r.health.failure(ctx, endpoint, callErr)
			return openai.Video{}, VideoBinding{}, callErr
		}
		r.health.success(ctx, endpoint)
		video.Model = input.Model
		return video, VideoBinding{Endpoint: endpoint.Name, Model: input.Model, Deployment: responseDeploymentIdentity(endpoint)}, nil
	}
	return openai.Video{}, VideoBinding{}, errors.New("no eligible video deployment")
}

func (r Router) videoClient(binding VideoBinding) (Endpoint, VideoClient, error) {
	for _, endpoint := range r.runtimeEndpoints() {
		if endpoint.Name == binding.Endpoint && binding.Model != "" && binding.Deployment == responseDeploymentIdentity(endpoint) && endpoint.supportsModel(binding.Model) && endpoint.supportsCapabilities("video") {
			if client, ok := endpoint.Provider.(VideoClient); ok {
				return endpoint, client, nil
			}
		}
	}
	return Endpoint{}, nil, ErrVideoDeploymentChanged
}

func (r Router) RetrieveVideo(ctx context.Context, binding VideoBinding, id string) (openai.Video, error) {
	video, err := callVideoLifecycle(r, ctx, binding, "video.retrieve", func(ctx context.Context, client VideoClient) (openai.Video, error) {
		return client.RetrieveVideo(ctx, id)
	})
	video.Model = binding.Model
	return video, err
}

func (r Router) DeleteVideo(ctx context.Context, binding VideoBinding, id string) (openai.VideoDeletion, error) {
	return callVideoLifecycle(r, ctx, binding, "video.delete", func(ctx context.Context, client VideoClient) (openai.VideoDeletion, error) {
		return client.DeleteVideo(ctx, id)
	})
}

func (r Router) DownloadVideoContent(ctx context.Context, binding VideoBinding, id, variant string) (VideoContent, error) {
	return callVideoLifecycle(r, ctx, binding, "video.content", func(ctx context.Context, client VideoClient) (VideoContent, error) {
		return client.DownloadVideoContent(ctx, id, variant)
	})
}

func (r Router) RemixVideo(ctx context.Context, identity modules.RequestContext, binding VideoBinding, id string, input openai.VideoRemixRequest, admit func(context.Context, *modules.RequestContext) error) (openai.Video, VideoBinding, error) {
	endpoint, client, err := r.videoClient(binding)
	if err != nil {
		return openai.Video{}, VideoBinding{}, err
	}
	release, err := r.acquireEndpoint(ctx, endpoint, 0)
	if err != nil {
		return openai.Video{}, VideoBinding{}, err
	}
	defer release()
	if err = r.health.permit(ctx, endpoint); err != nil {
		return openai.Video{}, VideoBinding{}, err
	}
	attempt := providerAttemptContext(identity, endpoint)
	attempt.Request.Model = binding.Model
	attempt.Metadata["gateway.api_type"] = "video"
	r.applyCatalogPricing(ctx, &attempt, endpoint, binding.Model)
	if admit != nil {
		if err = admit(ctx, &attempt); err != nil {
			return openai.Video{}, VideoBinding{}, err
		}
	}
	providerCtx, finish := r.startProviderCall(ctx, endpoint, "video.remix")
	video, err := client.RemixVideo(providerCtx, id, input)
	finish(err)
	if err != nil {
		r.health.failure(ctx, endpoint, err)
	} else {
		r.health.success(ctx, endpoint)
	}
	video.Model = binding.Model
	return video, binding, err
}

func callVideoLifecycle[T any](r Router, ctx context.Context, binding VideoBinding, operation string, call func(context.Context, VideoClient) (T, error)) (T, error) {
	var zero T
	endpoint, client, err := r.videoClient(binding)
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
