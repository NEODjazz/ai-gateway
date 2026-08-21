package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"ai-gateway-gateway/internal/modules"
)

type SessionStore interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
}

type affinityStore interface {
	get(ctx context.Context, key string) (string, bool, error)
	set(ctx context.Context, key, endpoint string) error
}

type memoryAffinityEntry struct {
	endpoint  string
	expiresAt time.Time
}

type memoryAffinity struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]memoryAffinityEntry
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
	return &memoryAffinity{ttl: ttl, entries: map[string]memoryAffinityEntry{}, now: time.Now}
}

func (s *memoryAffinity) get(_ context.Context, key string) (string, bool, error) {
	if s == nil || key == "" {
		return "", false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, found := s.entries[key]
	if !found || !s.now().Before(entry.expiresAt) {
		delete(s.entries, key)
		return "", false, nil
	}
	return entry.endpoint, true, nil
}

func (s *memoryAffinity) set(_ context.Context, key, endpoint string) error {
	if s == nil || key == "" || endpoint == "" {
		return nil
	}
	s.mu.Lock()
	s.entries[key] = memoryAffinityEntry{endpoint: endpoint, expiresAt: s.now().Add(s.ttl)}
	s.mu.Unlock()
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
	tenant := req.CredentialID
	if req.TeamID != "" {
		tenant = "team:" + req.TeamID
	}
	if tenant == "" || responseID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(tenant + "\x00" + responseID))
	return "responses-affinity:" + hex.EncodeToString(sum[:])
}
