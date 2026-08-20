package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"ai-gateway-gateway/internal/modules"
)

type exactCacheEntry struct {
	value     []byte
	expiresAt time.Time
}

type exactCache struct {
	mu       sync.Mutex
	ttl      time.Duration
	entries  map[string]exactCacheEntry
	now      func() time.Time
	maxBytes int
}

type ExactCacheStore interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
}

type responseCache interface {
	get(ctx context.Context, key string) ([]byte, bool, error)
	set(ctx context.Context, key string, value []byte) error
}

type distributedExactCache struct {
	store    ExactCacheStore
	ttl      time.Duration
	maxBytes int
}

func newResponseCache(ttl time.Duration, maxBytes int, store ExactCacheStore) responseCache {
	if ttl <= 0 {
		return nil
	}
	if store != nil {
		return distributedExactCache{store: store, ttl: ttl, maxBytes: maxBytes}
	}
	return newExactCacheWithLimit(ttl, maxBytes)
}

func newExactCache(ttl time.Duration) *exactCache {
	return newExactCacheWithLimit(ttl, 1_048_576)
}

func newExactCacheWithLimit(ttl time.Duration, maxBytes int) *exactCache {
	if ttl <= 0 {
		return nil
	}
	return &exactCache{ttl: ttl, entries: map[string]exactCacheEntry{}, now: time.Now, maxBytes: maxBytes}
}

func (c *exactCache) get(_ context.Context, key string) ([]byte, bool, error) {
	if c == nil || key == "" {
		return nil, false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, found := c.entries[key]
	if !found || !c.now().Before(entry.expiresAt) {
		delete(c.entries, key)
		return nil, false, nil
	}
	return append([]byte(nil), entry.value...), true, nil
}

func (c *exactCache) set(_ context.Context, key string, value []byte) error {
	if c == nil || key == "" {
		return nil
	}
	if c.maxBytes > 0 && len(value) > c.maxBytes {
		return nil
	}
	c.mu.Lock()
	c.entries[key] = exactCacheEntry{value: append([]byte(nil), value...), expiresAt: c.now().Add(c.ttl)}
	c.mu.Unlock()
	return nil
}

func (c distributedExactCache) get(ctx context.Context, key string) ([]byte, bool, error) {
	return c.store.Get(ctx, key)
}

func (c distributedExactCache) set(ctx context.Context, key string, value []byte) error {
	if c.maxBytes > 0 && len(value) > c.maxBytes {
		return nil
	}
	return c.store.Set(ctx, key, value, c.ttl)
}

func providerCacheKey(kind string, req modules.RequestContext) string {
	tenant := req.CredentialID
	if req.TeamID != "" {
		tenant = "team:" + req.TeamID
	}
	if tenant == "" {
		return ""
	}
	request := req.Request
	if logicalModel := req.Metadata["provider.requested_model"]; logicalModel != "" {
		request.Model = logicalModel
	}
	var value any = request
	if kind == "responses" && req.ResponseRequest != nil {
		responseRequest := *req.ResponseRequest
		if logicalModel := req.Metadata["provider.requested_model"]; logicalModel != "" {
			responseRequest.Model = logicalModel
		}
		value = responseRequest
	}
	body, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(append([]byte(kind+"\x00"+tenant+"\x00"), body...))
	return hex.EncodeToString(sum[:])
}

func decodeCached[T any](payload []byte) (T, bool) {
	var result T
	if json.Unmarshal(payload, &result) != nil {
		return result, false
	}
	return result, true
}

func (r Router) cacheGet(ctx context.Context, key string) ([]byte, bool, error) {
	if r.cache == nil || key == "" {
		return nil, false, nil
	}
	return r.cache.get(ctx, key)
}

func (r Router) cacheSet(ctx context.Context, key string, value []byte) error {
	if r.cache == nil || key == "" {
		return nil
	}
	return r.cache.set(ctx, key, value)
}
