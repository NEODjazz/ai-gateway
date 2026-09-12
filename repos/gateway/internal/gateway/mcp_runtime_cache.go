package gateway

import (
	"crypto/sha256"
	"sync"
	"time"
)

const (
	defaultMCPRuntimeCacheEntries = 64
	defaultMCPRuntimeCacheTTL     = 10 * time.Minute
)

type mcpRuntimeCacheEntry struct {
	client    MCPRuntimeClient
	expiresAt time.Time
	lastUsed  uint64
}

type mcpRuntimeCache struct {
	mu         sync.Mutex
	entries    map[[32]byte]mcpRuntimeCacheEntry
	maxEntries int
	ttl        time.Duration
	now        func() time.Time
	sequence   uint64
}

func newMCPRuntimeCache(maxEntries int, ttl time.Duration) *mcpRuntimeCache {
	return &mcpRuntimeCache{entries: make(map[[32]byte]mcpRuntimeCacheEntry), maxEntries: maxEntries, ttl: ttl, now: time.Now}
}

func mcpRuntimeCacheKey(endpoint, bearerToken string) [32]byte {
	return sha256.Sum256([]byte(endpoint + "\x00" + bearerToken))
}

func (c *mcpRuntimeCache) get(endpoint, bearerToken string, factory MCPRuntimeFactory) (MCPRuntimeClient, error) {
	if c == nil || c.maxEntries <= 0 || c.ttl <= 0 {
		return factory(endpoint, bearerToken)
	}
	key := mcpRuntimeCacheKey(endpoint, bearerToken)
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for candidate, entry := range c.entries {
		if !entry.expiresAt.After(now) {
			delete(c.entries, candidate)
		}
	}
	c.sequence++
	if entry, ok := c.entries[key]; ok {
		entry.expiresAt = now.Add(c.ttl)
		entry.lastUsed = c.sequence
		c.entries[key] = entry
		return entry.client, nil
	}
	client, err := factory(endpoint, bearerToken)
	if err != nil {
		return nil, err
	}
	if len(c.entries) >= c.maxEntries {
		var oldestKey [32]byte
		oldestSequence := ^uint64(0)
		for candidate, entry := range c.entries {
			if entry.lastUsed < oldestSequence {
				oldestKey, oldestSequence = candidate, entry.lastUsed
			}
		}
		delete(c.entries, oldestKey)
	}
	c.entries[key] = mcpRuntimeCacheEntry{client: client, expiresAt: now.Add(c.ttl), lastUsed: c.sequence}
	return client, nil
}

func (c *mcpRuntimeCache) invalidate(endpoint, bearerToken string) {
	if c == nil {
		return
	}
	key := mcpRuntimeCacheKey(endpoint, bearerToken)
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}
