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

func mcpRuntimeCacheKey(transport, endpoint, bearerToken string) [32]byte {
	return sha256.Sum256([]byte(transport + "\x00" + endpoint + "\x00" + bearerToken))
}

func (c *mcpRuntimeCache) get(transport, endpoint, bearerToken string, factory MCPRuntimeFactory) (MCPRuntimeClient, error) {
	if c == nil || c.maxEntries <= 0 || c.ttl <= 0 {
		return factory(transport, endpoint, bearerToken)
	}
	key := mcpRuntimeCacheKey(transport, endpoint, bearerToken)
	c.mu.Lock()
	now := c.now()
	var clientsToClose []MCPRuntimeClient
	for candidate, entry := range c.entries {
		if !entry.expiresAt.After(now) {
			clientsToClose = append(clientsToClose, entry.client)
			delete(c.entries, candidate)
		}
	}
	c.sequence++
	if entry, ok := c.entries[key]; ok {
		entry.expiresAt = now.Add(c.ttl)
		entry.lastUsed = c.sequence
		c.entries[key] = entry
		c.mu.Unlock()
		closeMCPRuntimeClients(clientsToClose)
		return entry.client, nil
	}
	client, err := factory(transport, endpoint, bearerToken)
	if err != nil {
		c.mu.Unlock()
		closeMCPRuntimeClients(clientsToClose)
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
		clientsToClose = append(clientsToClose, c.entries[oldestKey].client)
		delete(c.entries, oldestKey)
	}
	c.entries[key] = mcpRuntimeCacheEntry{client: client, expiresAt: now.Add(c.ttl), lastUsed: c.sequence}
	c.mu.Unlock()
	closeMCPRuntimeClients(clientsToClose)
	return client, nil
}

func (c *mcpRuntimeCache) invalidate(transport, endpoint, bearerToken string) {
	if c == nil {
		return
	}
	key := mcpRuntimeCacheKey(transport, endpoint, bearerToken)
	c.mu.Lock()
	client := c.entries[key].client
	delete(c.entries, key)
	c.mu.Unlock()
	closeMCPRuntimeClient(client)
}

func closeMCPRuntimeClients(clients []MCPRuntimeClient) {
	for _, client := range clients {
		closeMCPRuntimeClient(client)
	}
}

func closeMCPRuntimeClient(client MCPRuntimeClient) {
	if closer, ok := client.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}
