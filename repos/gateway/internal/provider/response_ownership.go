package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

var ErrResponseOwnershipConflict = errors.New("response ownership conflict")

var ErrResponseOwnershipUnavailable = errors.New("response ownership storage is unavailable")

var ErrResponseNotFound = errors.New("response not found")

var ErrResponseDeploymentChanged = errors.New("response deployment changed")

// Ownership is separate from optional routing affinity. Lifecycle operations
// must never infer ownership from an upstream ID or fall back on a cache miss.
type responseOwnership struct {
	Endpoint   string `json:"endpoint"`
	Model      string `json:"model"`
	Deployment string `json:"deployment"`
}

type responseOwnershipStore struct {
	store interface {
		Get(context.Context, string) ([]byte, bool, error)
		SetIfAbsentOrEqual(context.Context, string, []byte, time.Duration) (bool, error)
	}
	ttl time.Duration
}

func newResponseOwnershipStore(ttl time.Duration, store SessionStore) responseOwnershipStore {
	immutable, _ := store.(interface {
		Get(context.Context, string) ([]byte, bool, error)
		SetIfAbsentOrEqual(context.Context, string, []byte, time.Duration) (bool, error)
	})
	return responseOwnershipStore{store: immutable, ttl: ttl}
}

func (s responseOwnershipStore) configured() bool {
	return !interfaceIsNil(s.store) && s.ttl > 0
}

func persistentResponseRequested(request openai.ResponseRequest) bool {
	return request.Store != nil && *request.Store
}

func (r Router) validateResponseOwnership(req modules.RequestContext, request openai.ResponseRequest) error {
	if !persistentResponseRequested(request) {
		return nil
	}
	if !r.ownership.configured() || req.CredentialID == "" {
		return ErrResponseOwnershipUnavailable
	}
	return nil
}

func (r Router) persistResponseOwnership(ctx context.Context, req modules.RequestContext, request openai.ResponseRequest, model, responseID string, endpoint Endpoint) error {
	if !persistentResponseRequested(request) {
		return nil
	}
	binding := responseOwnership{
		Endpoint:   endpoint.Name,
		Model:      model,
		Deployment: responseDeploymentIdentity(endpoint),
	}
	if err := r.ownership.put(ctx, req, responseID, binding); err != nil {
		if errors.Is(err, ErrResponseOwnershipConflict) {
			return err
		}
		return ErrResponseOwnershipUnavailable
	}
	return nil
}

func responseDeploymentIdentity(endpoint Endpoint) string {
	payload, _ := json.Marshal([]string{endpoint.Name, endpoint.ProviderID, endpoint.Type, endpoint.BaseURL, endpoint.CredentialID})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func responseOwnershipKey(req modules.RequestContext, id string) string {
	if !validResponseResourceID(id) {
		return ""
	}
	key := affinityKey(req, id)
	if key == "" {
		return ""
	}
	return "response-owner:v1:" + key
}

func (s responseOwnershipStore) put(ctx context.Context, req modules.RequestContext, id string, binding responseOwnership) error {
	key := responseOwnershipKey(req, id)
	if key == "" || binding.Endpoint == "" || binding.Model == "" || len(binding.Deployment) != 64 {
		return errors.New("invalid response ownership record")
	}
	if interfaceIsNil(s.store) || s.ttl <= 0 {
		return ErrResponseOwnershipUnavailable
	}
	payload, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	if len(payload) > 4096 {
		return errors.New("response ownership record exceeds limit")
	}
	accepted, err := s.store.SetIfAbsentOrEqual(ctx, key, payload, s.ttl)
	if err != nil {
		return ErrResponseOwnershipUnavailable
	}
	if !accepted {
		return ErrResponseOwnershipConflict
	}
	return nil
}

func (s responseOwnershipStore) get(ctx context.Context, req modules.RequestContext, id string) (responseOwnership, bool, error) {
	key := responseOwnershipKey(req, id)
	if key == "" {
		return responseOwnership{}, false, nil
	}
	if interfaceIsNil(s.store) || s.ttl <= 0 {
		return responseOwnership{}, false, ErrResponseOwnershipUnavailable
	}
	payload, found, err := s.store.Get(ctx, key)
	if err != nil {
		return responseOwnership{}, false, ErrResponseOwnershipUnavailable
	}
	if !found {
		return responseOwnership{}, false, nil
	}
	var binding responseOwnership
	if len(payload) > 4096 || json.Unmarshal(payload, &binding) != nil || binding.Endpoint == "" || binding.Model == "" || len(binding.Deployment) != 64 {
		return responseOwnership{}, false, ErrResponseOwnershipUnavailable
	}
	return binding, true, nil
}

type responseResourceClient interface {
	RetrieveResponse(context.Context, string) (openai.ResponseResponse, error)
}

func (r Router) responseResource(ctx context.Context, req modules.RequestContext, id string) (responseOwnership, Endpoint, responseResourceClient, error) {
	binding, found, err := r.ownership.get(ctx, req, id)
	if err != nil {
		return responseOwnership{}, Endpoint{}, nil, err
	}
	if !found {
		return responseOwnership{}, Endpoint{}, nil, ErrResponseNotFound
	}
	for _, endpoint := range r.runtimeEndpoints() {
		if endpoint.Name != binding.Endpoint {
			continue
		}
		if responseDeploymentIdentity(endpoint) != binding.Deployment || !endpoint.supportsModel(binding.Model) || !endpoint.supportsCapabilities("responses") {
			return responseOwnership{}, Endpoint{}, nil, ErrResponseDeploymentChanged
		}
		client, ok := endpoint.Provider.(responseResourceClient)
		if !ok {
			return responseOwnership{}, Endpoint{}, nil, ErrResponseDeploymentChanged
		}
		return binding, endpoint, client, nil
	}
	return responseOwnership{}, Endpoint{}, nil, ErrResponseDeploymentChanged
}

func (r Router) ResolveResponseResource(ctx context.Context, req modules.RequestContext, id string) (string, error) {
	binding, _, _, err := r.responseResource(ctx, req, id)
	if err != nil {
		return "", err
	}
	return binding.Model, nil
}

func (r Router) RetrieveResponse(ctx context.Context, req modules.RequestContext, id string) (openai.ResponseResponse, error) {
	_, endpoint, client, err := r.responseResource(ctx, req, id)
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	release, err := endpoint.Admission.acquire(ctx, endpoint.Name)
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	defer release()
	if err := r.health.permit(ctx, endpoint); err != nil {
		return openai.ResponseResponse{}, err
	}
	for attempt := 0; attempt <= endpointMaxRetries(endpoint); attempt++ {
		callCtx, finish := r.startProviderCall(ctx, endpoint, "responses.retrieve")
		response, callErr := client.RetrieveResponse(callCtx, id)
		finish(callErr)
		if callErr == nil {
			r.health.success(ctx, endpoint)
			return response, nil
		}
		if ctx.Err() != nil || attempt >= endpointRetryLimit(endpoint, callErr) || !retrySameEndpointWithPolicy(endpoint, callErr) {
			r.health.failure(ctx, endpoint, callErr)
			return openai.ResponseResponse{}, callErr
		}
		if waitErr := r.retry.beforeRetry(ctx, callErr, attempt); waitErr != nil {
			r.health.failure(ctx, endpoint, waitErr)
			return openai.ResponseResponse{}, waitErr
		}
	}
	return openai.ResponseResponse{}, errors.New("response retrieval failed")
}
