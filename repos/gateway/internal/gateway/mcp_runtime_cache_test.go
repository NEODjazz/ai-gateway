package gateway

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMCPRuntimeCacheIsBoundedCredentialIsolatedAndExpires(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cache := newMCPRuntimeCache(2, time.Minute)
	cache.now = func() time.Time { return now }
	var calls int
	factory := func(string, string) (MCPRuntimeClient, error) {
		calls++
		return &fakeMCPRuntimeClient{}, nil
	}
	first, _ := cache.get("https://mcp.example.test", "credential-a", factory)
	reused, _ := cache.get("https://mcp.example.test", "credential-a", factory)
	if first != reused || calls != 1 {
		t.Fatalf("client was not reused: calls=%d", calls)
	}
	_, _ = cache.get("https://mcp.example.test", "credential-b", factory)
	_, _ = cache.get("https://mcp.example.test", "credential-a", factory)
	_, _ = cache.get("https://other.example.test", "credential-c", factory)
	if calls != 3 || len(cache.entries) != 2 {
		t.Fatalf("bounded cache calls=%d entries=%d", calls, len(cache.entries))
	}
	_, _ = cache.get("https://mcp.example.test", "credential-b", factory)
	if calls != 4 || len(cache.entries) != 2 {
		t.Fatalf("LRU entry was not evicted: calls=%d entries=%d", calls, len(cache.entries))
	}
	now = now.Add(2 * time.Minute)
	_, _ = cache.get("https://mcp.example.test", "credential-a", factory)
	if calls != 5 || len(cache.entries) != 1 {
		t.Fatalf("expired entries were retained: calls=%d entries=%d", calls, len(cache.entries))
	}
}

func TestMCPRuntimeCacheCreatesOneClientConcurrently(t *testing.T) {
	cache := newMCPRuntimeCache(4, time.Minute)
	var calls atomic.Int32
	factory := func(string, string) (MCPRuntimeClient, error) {
		calls.Add(1)
		return &fakeMCPRuntimeClient{}, nil
	}
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := cache.get("https://mcp.example.test", "credential", factory); err != nil {
				t.Errorf("get: %v", err)
			}
		}()
	}
	wait.Wait()
	if calls.Load() != 1 || len(cache.entries) != 1 {
		t.Fatalf("factory calls=%d entries=%d", calls.Load(), len(cache.entries))
	}
}
