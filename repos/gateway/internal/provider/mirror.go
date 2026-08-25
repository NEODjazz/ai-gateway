package provider

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"time"

	"ai-gateway-gateway/internal/modelcatalog"
	"ai-gateway-gateway/internal/openai"
)

func (r Router) mirrorChat(ctx context.Context, requestID string, request openai.ChatCompletionRequest, requestedModel string, capabilities ...string) {
	catalog := r.catalog.Current(ctx)
	for _, endpoint := range r.shadowEndpoints(catalog, requestedModel, capabilities...) {
		if !mirrorSample(requestID, requestedModel, endpoint) {
			continue
		}
		mirrored, ok := cloneMirrorRequest(request)
		if !ok {
			continue
		}
		mirrored.Provider = ""
		mirrored.Model = requestedModel
		mirrored.Stream = false
		if upstream, found := endpoint.ModelAliases[mirrored.Model]; found {
			mirrored.Model = upstream
		}
		go r.runMirror(ctx, endpoint, "chat.mirror", func(callCtx context.Context) error {
			_, err := endpoint.Provider.ChatCompletions(callCtx, mirrored)
			return err
		})
	}
}

func (r Router) mirrorResponses(ctx context.Context, requestID string, request openai.ResponseRequest, requestedModel string, capabilities ...string) {
	catalog := r.catalog.Current(ctx)
	for _, endpoint := range r.shadowEndpoints(catalog, requestedModel, capabilities...) {
		if !mirrorSample(requestID, requestedModel, endpoint) {
			continue
		}
		mirrored, ok := cloneMirrorRequest(request)
		if !ok {
			continue
		}
		mirrored.Provider = ""
		mirrored.Model = requestedModel
		mirrored.Stream = false
		if upstream, found := endpoint.ModelAliases[mirrored.Model]; found {
			mirrored.Model = upstream
		}
		go r.runMirror(ctx, endpoint, "responses.mirror", func(callCtx context.Context) error {
			_, err := endpoint.Provider.Responses(callCtx, mirrored)
			return err
		})
	}
}

func (r Router) mirrorEmbeddings(ctx context.Context, requestID string, request openai.EmbeddingRequest, requestedModel string) {
	catalog := r.catalog.Current(ctx)
	for _, endpoint := range r.shadowEndpoints(catalog, requestedModel, "embeddings") {
		client, ok := endpoint.Provider.(EmbeddingClient)
		if !ok || !mirrorSample(requestID, requestedModel, endpoint) {
			continue
		}
		mirrored, cloned := cloneMirrorRequest(request)
		if !cloned {
			continue
		}
		mirrored.Provider = ""
		mirrored.Model = requestedModel
		if upstream, found := endpoint.ModelAliases[mirrored.Model]; found {
			mirrored.Model = upstream
		}
		go r.runMirror(ctx, endpoint, "embeddings.mirror", func(callCtx context.Context) error { _, err := client.Embeddings(callCtx, mirrored); return err })
	}
}

func (r Router) shadowEndpoints(catalog modelcatalog.Catalog, model string, capabilities ...string) []Endpoint {
	result := make([]Endpoint, 0)
	for _, endpoint := range r.endpoints {
		if !endpoint.Shadow || !endpoint.supportsModel(model) || !supportsCatalogCapabilities(catalog, endpoint, model, capabilities...) {
			continue
		}
		result = append(result, endpoint)
	}
	return result
}

func (r Router) runMirror(parent context.Context, endpoint Endpoint, operation string, call func(context.Context) error) {
	timeout := endpoint.MirrorTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	defer cancel()
	release, err := endpoint.Admission.acquire(ctx, endpoint.Name)
	if err != nil {
		return
	}
	defer release()
	callCtx, finish := r.startProviderCall(ctx, endpoint, operation)
	err = call(callCtx)
	finish(err)
}

func mirrorSample(requestID, model string, endpoint Endpoint) bool {
	if endpoint.MirrorPercentage >= 100 {
		return true
	}
	if endpoint.MirrorPercentage <= 0 {
		return false
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(requestID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(model))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(endpoint.Name))
	return h.Sum64()%10000 < uint64(endpoint.MirrorPercentage*100)
}

func cloneMirrorRequest[T any](value T) (T, bool) {
	var cloned T
	payload, err := json.Marshal(value)
	if err != nil {
		return cloned, false
	}
	if err := json.Unmarshal(payload, &cloned); err != nil {
		return cloned, false
	}
	return cloned, true
}
