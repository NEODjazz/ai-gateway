package provider

import (
	"context"
	"errors"
	"fmt"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

var ErrFineTuningDeploymentChanged = errors.New("fine-tuning deployment changed")

func (r Router) CreateFineTuningJob(ctx context.Context, _ modules.RequestContext, input openai.FineTuningCreateRequest) (openai.FineTuningJob, FineTuningBinding, error) {
	candidates := r.candidates(ctx, openai.ChatCompletionRequest{Model: input.Model}, "fine_tuning")
	for _, endpoint := range candidates {
		client, ok := endpoint.Provider.(FineTuningClient)
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
		request := input
		if alias, found := endpoint.ModelAliases[input.Model]; found {
			request.Model = alias
		}
		providerCtx, finish := r.startProviderCall(ctx, endpoint, "fine_tuning.create")
		job, callErr := client.CreateFineTuningJob(providerCtx, request)
		finish(callErr)
		release()
		if callErr != nil {
			r.health.failure(ctx, endpoint, callErr)
			return openai.FineTuningJob{}, FineTuningBinding{}, fmt.Errorf("%s/%s fine-tuning create failed: %w", endpoint.Type, endpoint.Name, callErr)
		}
		r.health.success(ctx, endpoint)
		job.Model = input.Model
		return job, FineTuningBinding{Endpoint: endpoint.Name, Model: input.Model, Deployment: responseDeploymentIdentity(endpoint)}, nil
	}
	return openai.FineTuningJob{}, FineTuningBinding{}, errors.New("no eligible fine-tuning deployment")
}

func (r Router) fineTuningClient(binding FineTuningBinding) (Endpoint, FineTuningClient, error) {
	for _, endpoint := range r.runtimeEndpoints() {
		if endpoint.Name == binding.Endpoint && binding.Model != "" && binding.Deployment == responseDeploymentIdentity(endpoint) && endpoint.supportsModel(binding.Model) && endpoint.supportsCapabilities("fine_tuning") {
			client, ok := endpoint.Provider.(FineTuningClient)
			if ok {
				return endpoint, client, nil
			}
		}
	}
	return Endpoint{}, nil, ErrFineTuningDeploymentChanged
}

func callFineTuningLifecycle[T any](r Router, ctx context.Context, binding FineTuningBinding, operation string, call func(context.Context, FineTuningClient) (T, error)) (T, error) {
	var zero T
	endpoint, client, err := r.fineTuningClient(binding)
	if err != nil {
		return zero, err
	}
	release, err := r.acquireEndpoint(ctx, endpoint, 0)
	if err != nil {
		return zero, err
	}
	defer release()
	if err := r.health.permit(ctx, endpoint); err != nil {
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

func (r Router) RetrieveFineTuningJob(ctx context.Context, binding FineTuningBinding, id string) (openai.FineTuningJob, error) {
	return callFineTuningLifecycle(r, ctx, binding, "fine_tuning.retrieve", func(ctx context.Context, client FineTuningClient) (openai.FineTuningJob, error) {
		return client.RetrieveFineTuningJob(ctx, id)
	})
}
func (r Router) CancelFineTuningJob(ctx context.Context, binding FineTuningBinding, id string) (openai.FineTuningJob, error) {
	return callFineTuningLifecycle(r, ctx, binding, "fine_tuning.cancel", func(ctx context.Context, client FineTuningClient) (openai.FineTuningJob, error) {
		return client.CancelFineTuningJob(ctx, id)
	})
}
func (r Router) PauseFineTuningJob(ctx context.Context, binding FineTuningBinding, id string) (openai.FineTuningJob, error) {
	return callFineTuningLifecycle(r, ctx, binding, "fine_tuning.pause", func(ctx context.Context, client FineTuningClient) (openai.FineTuningJob, error) {
		return client.PauseFineTuningJob(ctx, id)
	})
}
func (r Router) ResumeFineTuningJob(ctx context.Context, binding FineTuningBinding, id string) (openai.FineTuningJob, error) {
	return callFineTuningLifecycle(r, ctx, binding, "fine_tuning.resume", func(ctx context.Context, client FineTuningClient) (openai.FineTuningJob, error) {
		return client.ResumeFineTuningJob(ctx, id)
	})
}
func (r Router) ListFineTuningEvents(ctx context.Context, binding FineTuningBinding, id string, options FineTuningListOptions) (openai.FineTuningEventList, error) {
	return callFineTuningLifecycle(r, ctx, binding, "fine_tuning.events", func(ctx context.Context, client FineTuningClient) (openai.FineTuningEventList, error) {
		return client.ListFineTuningEvents(ctx, id, options)
	})
}
func (r Router) ListFineTuningCheckpoints(ctx context.Context, binding FineTuningBinding, id string, options FineTuningListOptions) (openai.FineTuningCheckpointList, error) {
	return callFineTuningLifecycle(r, ctx, binding, "fine_tuning.checkpoints", func(ctx context.Context, client FineTuningClient) (openai.FineTuningCheckpointList, error) {
		return client.ListFineTuningCheckpoints(ctx, id, options)
	})
}

func (r Router) DeleteFineTunedModel(ctx context.Context, binding FineTuningBinding, model string) (openai.ModelDeletion, error) {
	return callFineTuningLifecycle(r, ctx, binding, "fine_tuning.model.delete", func(ctx context.Context, client FineTuningClient) (openai.ModelDeletion, error) {
		return client.DeleteFineTunedModel(ctx, model)
	})
}
