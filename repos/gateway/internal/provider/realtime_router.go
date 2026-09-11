package provider

import (
	"context"
	"errors"
	"sync"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func (r Router) OpenRealtime(ctx context.Context, identity modules.RequestContext, model string) (RealtimeConnection, modules.RequestContext, error) {
	request := openai.ChatCompletionRequest{Model: model}
	identity.Request.Model = model
	candidates := r.routeCandidates(ctx, identity, request, "realtime")
	progress := newRouteProgress(candidates)
	var lastErr error
	for _, endpoint := range candidates {
		if !progress.allows(endpoint) {
			continue
		}
		progress.enter(endpoint)
		client, ok := endpoint.Provider.(RealtimeClient)
		if !ok {
			continue
		}
		release, err := endpoint.Admission.acquire(ctx, endpoint.Name)
		if err != nil {
			lastErr = err
			progress.fail(err)
			continue
		}
		if err = r.health.permit(ctx, endpoint); err != nil {
			release()
			lastErr = err
			progress.fail(err)
			continue
		}
		attempt := providerAttemptContext(identity, endpoint)
		attempt.Metadata["gateway.api_type"] = "realtime"
		r.applyCatalogPricing(ctx, &attempt, endpoint, model)
		providerCtx, finish := r.startProviderCall(ctx, endpoint, "realtime.session")
		connection, callErr := client.OpenRealtime(providerCtx, attempt.Request.Model)
		if callErr != nil {
			finish(callErr)
			release()
			r.health.failure(ctx, endpoint, callErr)
			lastErr = callErr
			progress.fail(callErr)
			continue
		}
		r.health.success(ctx, endpoint)
		return &routedRealtimeConnection{
			RealtimeConnection: connection,
			reserve: func(reserveCtx context.Context, tokens int) error {
				return r.reserveEndpointQuota(reserveCtx, endpoint, tokens)
			},
			finish: func(sessionErr error) {
				finish(sessionErr)
				release()
				if sessionErr != nil {
					r.health.failure(context.WithoutCancel(ctx), endpoint, sessionErr)
				}
			},
		}, attempt, nil
	}
	if lastErr != nil {
		return nil, modules.RequestContext{}, lastErr
	}
	return nil, modules.RequestContext{}, errors.New("no eligible realtime deployment")
}

type routedRealtimeConnection struct {
	RealtimeConnection
	once    sync.Once
	finish  func(error)
	reserve func(context.Context, int) error
}

func (c *routedRealtimeConnection) ReserveRealtimeTokens(ctx context.Context, tokens int) error {
	return c.reserve(ctx, tokens)
}

func (c *routedRealtimeConnection) Receive() ([]byte, error) {
	payload, err := c.RealtimeConnection.Receive()
	if err != nil {
		c.once.Do(func() { c.finish(err) })
	}
	return payload, err
}

func (c *routedRealtimeConnection) Close() error {
	err := c.RealtimeConnection.Close()
	c.once.Do(func() { c.finish(nil) })
	return err
}
