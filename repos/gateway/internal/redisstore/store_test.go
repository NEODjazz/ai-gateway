package redisstore

import (
	"context"
	"os"
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
