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
