package gateway

import (
	"context"
	"testing"
	"time"
)

func TestMemoryRateLimitStoreResetsWindow(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := NewMemoryRateLimitStore()
	store.now = func() time.Time { return now }
	limit := RateLimit{Requests: 1, Tokens: 10}

	allowed, _, err := store.Allow(context.Background(), "key", limit, 5)
	if err != nil || !allowed {
		t.Fatalf("first request should pass: allowed=%v err=%v", allowed, err)
	}
	allowed, retryAfter, err := store.Allow(context.Background(), "key", limit, 5)
	if err != nil || allowed || retryAfter <= 0 {
		t.Fatalf("second request should be limited: allowed=%v retry=%s err=%v", allowed, retryAfter, err)
	}
	now = now.Add(time.Minute)
	allowed, _, err = store.Allow(context.Background(), "key", limit, 5)
	if err != nil || !allowed {
		t.Fatalf("request should pass after reset: allowed=%v err=%v", allowed, err)
	}
}

func TestModelAllowedSupportsPrefixGrant(t *testing.T) {
	if !modelAllowed("gpt-5.3", []string{"gpt-5.*"}) || modelAllowed("claude-sonnet", []string{"gpt-5.*"}) {
		t.Fatal("unexpected model grant matching")
	}
}
