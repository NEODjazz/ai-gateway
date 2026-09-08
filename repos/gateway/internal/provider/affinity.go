package provider

import (
	"ai-gateway-gateway/internal/bounded"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"ai-gateway-gateway/internal/modules"
)

// ErrResponseAffinityUnavailable prevents continuation routing when the stored
// endpoint binding cannot be read. A missing binding is a separate cache miss.
var ErrResponseAffinityUnavailable = errors.New("response affinity is unavailable")

type SessionStore interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
}

type affinityStore interface {
	get(ctx context.Context, key string) (string, bool, error)
	set(ctx context.Context, key, endpoint string) error
}

const memoryAffinityMaxEntries = 4096
const memoryAffinityMaxBytes = 2 << 20

type memoryAffinity struct {
	entries *bounded.Cache[string]
	now     func() time.Time
}

type distributedAffinity struct {
	store SessionStore
	ttl   time.Duration
}

func newAffinityStore(ttl time.Duration, store SessionStore) affinityStore {
	if ttl <= 0 {
		return nil
	}
	if !interfaceIsNil(store) {
		return distributedAffinity{store: store, ttl: ttl}
	}
	return &memoryAffinity{entries: bounded.New[string](ttl, memoryAffinityMaxEntries, memoryAffinityMaxBytes), now: time.Now}
}

func (s *memoryAffinity) get(_ context.Context, key string) (string, bool, error) {
	if s == nil || key == "" {
		return "", false, nil
	}
	value, found := s.entries.Get(key, s.now())
	return value, found, nil
}

func (s *memoryAffinity) set(_ context.Context, key, endpoint string) error {
	if s == nil || key == "" || endpoint == "" {
		return nil
	}
	s.entries.Set(key, endpoint, len(endpoint), s.now())
	return nil
}

func (s distributedAffinity) get(ctx context.Context, key string) (string, bool, error) {
	value, found, err := s.store.Get(ctx, key)
	return string(value), found, err
}

func (s distributedAffinity) set(ctx context.Context, key, endpoint string) error {
	return s.store.Set(ctx, key, []byte(endpoint), s.ttl)
}

func affinityKey(req modules.RequestContext, responseID string) string {
	tenant := req.CredentialID + "\x00" + req.UserID
	if req.CredentialID == "" || responseID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(tenant + "\x00" + responseID))
	return "responses-affinity:" + hex.EncodeToString(sum[:])
}
