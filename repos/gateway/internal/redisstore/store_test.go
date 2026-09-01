package redisstore

import (
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func integrationStore(t *testing.T) *Store {
	t.Helper()
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		server := miniredis.RunT(t)
		addr = server.Addr()
	}
	store := New(Config{Addr: addr, Prefix: "ai-gateway-test-" + time.Now().Format("150405.000000000")})
	if err := store.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestNilRedisStoreCacheMethodsReturnErrors(t *testing.T) {
	var store *Store
	if _, _, err := store.Get(context.Background(), "key"); err == nil {
		t.Fatal("nil store Get must fail without panicking")
	}
	if err := store.Set(context.Background(), "key", []byte("value"), 0); err == nil {
		t.Fatal("nil store Set must fail without panicking")
	}
}

func TestRedisCacheRoundTrip(t *testing.T) {
	store := integrationStore(t)
	ctx := context.Background()
	if err := store.Set(ctx, "cache-key", []byte("value"), time.Minute); err != nil {
		t.Fatal(err)
	}
	value, found, err := store.Get(ctx, "cache-key")
	if err != nil || !found || string(value) != "value" {
		t.Fatalf("unexpected cache result: value=%q found=%v err=%v", value, found, err)
	}
}

func TestRedisBoundedListIsSharedNewestFirstAndTrimmed(t *testing.T) {
	server := miniredis.RunT(t)
	first := New(Config{Addr: server.Addr(), Prefix: "shared-list"})
	second := New(Config{Addr: server.Addr(), Prefix: "shared-list"})
	ctx := context.Background()
	for _, value := range []string{"one", "two", "three"} {
		if err := first.PushBounded(ctx, "guardrail-events", []byte(value), 2, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	values, err := second.ListBounded(ctx, "guardrail-events", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || string(values[0]) != "three" || string(values[1]) != "two" {
		t.Fatalf("unexpected bounded list: %q", values)
	}
	for _, key := range server.Keys() {
		if strings.Contains(key, "guardrail-events") {
			t.Fatalf("list namespace leaked into Redis key: %q", key)
		}
	}
}

func TestRedisRateLimitIsAtomic(t *testing.T) {
	store := integrationStore(t)
	ctx := context.Background()
	var allowed atomic.Int64
	var group sync.WaitGroup
	for range 20 {
		group.Add(1)
		go func() {
			defer group.Done()
			ok, _, err := store.Allow(ctx, "tenant", 5, 100, 1, time.Minute)
			if err != nil {
				t.Errorf("allow failed: %v", err)
				return
			}
			if ok {
				allowed.Add(1)
			}
		}()
	}
	group.Wait()
	if allowed.Load() != 5 {
		t.Fatalf("expected exactly five atomic admissions, got %d", allowed.Load())
	}
}

func TestRedisCircuitIsSharedAndAllowsSingleHalfOpenProbe(t *testing.T) {
	server := miniredis.RunT(t)
	first := New(Config{Addr: server.Addr(), Prefix: "shared-circuit"})
	second := New(Config{Addr: server.Addr(), Prefix: "shared-circuit"})
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	cooldown := time.Minute

	if err := first.CircuitFailure(ctx, "ollama", 2, cooldown, now); err != nil {
		t.Fatal(err)
	}
	if available, err := second.CircuitAvailable(ctx, "ollama", now); err != nil || !available {
		t.Fatalf("circuit opened before threshold: available=%v err=%v", available, err)
	}
	if err := second.CircuitFailure(ctx, "ollama", 2, cooldown, now); err != nil {
		t.Fatal(err)
	}
	if available, err := first.CircuitAvailable(ctx, "ollama", now); err != nil || available {
		t.Fatalf("open circuit was not shared: available=%v err=%v", available, err)
	}

	afterCooldown := now.Add(cooldown)
	if allowed, err := first.CircuitPermit(ctx, "ollama", afterCooldown, 30*time.Second); err != nil || !allowed {
		t.Fatalf("first half-open probe was rejected: allowed=%v err=%v", allowed, err)
	}
	if allowed, err := second.CircuitPermit(ctx, "ollama", afterCooldown, 30*time.Second); err != nil || allowed {
		t.Fatalf("second half-open probe was allowed: allowed=%v err=%v", allowed, err)
	}
	if err := first.CircuitFailure(ctx, "ollama", 2, cooldown, afterCooldown); err != nil {
		t.Fatal(err)
	}
	if available, err := second.CircuitAvailable(ctx, "ollama", afterCooldown); err != nil || available {
		t.Fatalf("failed half-open probe did not reopen circuit: available=%v err=%v", available, err)
	}
	afterSecondCooldown := afterCooldown.Add(cooldown)
	if allowed, err := first.CircuitPermit(ctx, "ollama", afterSecondCooldown, 30*time.Second); err != nil || !allowed {
		t.Fatalf("second half-open cycle was rejected: allowed=%v err=%v", allowed, err)
	}
	if err := first.CircuitSuccess(ctx, "ollama"); err != nil {
		t.Fatal(err)
	}
	if allowed, err := second.CircuitPermit(ctx, "ollama", afterSecondCooldown, 30*time.Second); err != nil || !allowed {
		t.Fatalf("success did not close shared circuit: allowed=%v err=%v", allowed, err)
	}
	for _, key := range server.Keys() {
		if strings.Contains(key, "ollama") {
			t.Fatalf("endpoint name leaked into Redis key: %q", key)
		}
	}
}
