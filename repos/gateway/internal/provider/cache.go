package provider

import (
	"ai-gateway-gateway/internal/bounded"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"ai-gateway-gateway/internal/modules"
)

const memoryCacheMaxEntries = 1024
const memoryCacheMaxBytes = 64 << 20

type exactCache struct {
	ttl      time.Duration
	entries  *bounded.Cache[[]byte]
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
	if !interfaceIsNil(store) {
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
	return &exactCache{ttl: ttl, entries: bounded.New[[]byte](ttl, memoryCacheMaxEntries, memoryCacheMaxBytes), now: time.Now, maxBytes: maxBytes}
}

func (c *exactCache) get(_ context.Context, key string) ([]byte, bool, error) {
	if c == nil || key == "" {
		return nil, false, nil
	}
	payload, found := c.entries.Get(key, c.now())
	return append([]byte(nil), payload...), found, nil
}

func (c *exactCache) set(_ context.Context, key string, value []byte) error {
	if c == nil || key == "" || len(value) > memoryCacheMaxBytes || (c.maxBytes > 0 && len(value) > c.maxBytes) {
		return nil
	}
	c.entries.Set(key, append([]byte(nil), value...), len(value), c.now())
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
	tenant := cacheIsolationScope(req)
	if tenant == "" {
		return ""
	}
	request := req.Request
	if logicalModel := req.Metadata["provider.requested_model"]; logicalModel != "" {
		request.Model = logicalModel
	}
	var value any = request
	if kind == "chat" && request.RequireMatchedStop {
		kind = "chat-matched-stop"
	}
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
	sum := sha256.Sum256(append([]byte("v2\x00"+kind+"\x00"+tenant+"\x00"), body...))
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
		if r.observer != nil {
			r.observer.ObserveCache("get", "disabled")
		}
		return nil, false, nil
	}
	payload, found, err := r.cache.get(ctx, key)
	if r.observer != nil {
		result := "miss"
		if err != nil {
			result = "error"
		} else if found {
			result = "hit"
		}
		r.observer.ObserveCache("get", result)
	}
	return payload, found, err
}

func (r Router) cacheSet(ctx context.Context, key string, value []byte) error {
	if r.cache == nil || key == "" {
		if r.observer != nil {
			r.observer.ObserveCache("set", "disabled")
		}
		return nil
	}
	err := r.cache.set(ctx, key, value)
	if r.observer != nil {
		result := "ok"
		if err != nil {
			result = "error"
		}
		r.observer.ObserveCache("set", result)
	}
	return err
}

// cacheIsolationScope intentionally does not allow implicit team-wide sharing.
// Only effective policy metadata enters the fingerprint; request IDs and usage
// counters would prevent hits and are excluded.
func cacheIsolationScope(req modules.RequestContext) string {
	if req.CredentialID == "" {
		return ""
	}
	canonical := func(values []string) []string { v := append([]string{}, values...); sort.Strings(v); return v }
	policy := map[string]string{}
	for key, value := range req.Metadata {
		if strings.HasPrefix(key, "policy.") || strings.HasPrefix(key, "provider.modules.") || strings.HasPrefix(key, "provider.guardrail.") {
			policy[key] = value
		}
	}
	data, _ := json.Marshal(struct {
		Credential, User, Team, Organization                string
		Roles, Tags, Models, Tools, GroupModels, GroupTools []string
		GroupsEvaluated                                     bool
		Policy                                              map[string]string
	}{req.CredentialID, req.UserID, req.TeamID, req.OrganizationID, canonical(req.Roles), canonical(req.Tags), canonical(req.AllowedModels), canonical(req.AllowedTools), canonical(req.AccessGroupModels), canonical(req.AccessGroupTools), req.AccessGroupsEvaluated, policy})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
