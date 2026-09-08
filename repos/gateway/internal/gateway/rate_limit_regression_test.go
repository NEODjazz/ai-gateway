package gateway

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/redisstore"
	"github.com/alicebob/miniredis/v2"
)

func TestRateLimitStoreCounterContract(t *testing.T) {
	factories := map[string]func(*testing.T) RateLimitStore{
		"memory": func(t *testing.T) RateLimitStore { return NewMemoryRateLimitStore() },
		"redis": func(t *testing.T) RateLimitStore {
			addr := os.Getenv("REDIS_TEST_ADDR")
			if addr == "" {
				addr = miniredis.RunT(t).Addr()
			}
			return NewRedisRateLimitStore(redisstore.New(redisstore.Config{Addr: addr, Prefix: t.Name() + time.Now().Format("150405.000000000")}))
		},
	}
	type step struct {
		limit   RateLimit
		tokens  int
		allowed bool
	}
	cases := map[string][]step{
		"overflow rejection preserves counters": {
			{RateLimit{Requests: 2, Tokens: 100}, 1, true},
			{RateLimit{Requests: 2, Tokens: 100}, math.MaxInt, false},
			{RateLimit{Requests: 2, Tokens: 100}, 99, true},
			{RateLimit{Requests: 2, Tokens: 100}, 0, false},
		},
		"exact maximum": {
			{RateLimit{Tokens: math.MaxInt}, math.MaxInt - 1, true},
			{RateLimit{Tokens: math.MaxInt}, 1, true},
			{RateLimit{Tokens: math.MaxInt}, 1, false},
			{RateLimit{Tokens: math.MaxInt}, 0, true},
		},
		"unlimited token counter saturates": {
			{RateLimit{Requests: 10}, math.MaxInt, true},
			{RateLimit{Requests: 10}, 1, true},
			{RateLimit{Tokens: 100}, 0, false},
			{RateLimit{Tokens: math.MaxInt}, 0, true},
		},
		"negative tokens cannot refund usage": {
			{RateLimit{Tokens: 100}, 100, true},
			{RateLimit{Tokens: 100}, -100, true},
			{RateLimit{Tokens: 100}, 1, false},
		},
	}
	for backend, factory := range factories {
		t.Run(backend, func(t *testing.T) {
			for name, steps := range cases {
				t.Run(name, func(t *testing.T) {
					store := factory(t)
					for i, s := range steps {
						allowed, retry, err := store.Allow(context.Background(), "identity", s.limit, s.tokens)
						if err != nil || allowed != s.allowed || (!allowed && retry <= 0) {
							t.Fatalf("step %d: allowed=%v retry=%v err=%v, want allowed=%v", i, allowed, retry, err, s.allowed)
						}
					}
				})
			}
		})
	}
}

func TestMemoryRateLimitCapacityAndExpiry(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store := NewMemoryRateLimitStore()
	store.now = func() time.Time { return now }
	ctx := context.Background()
	limit := RateLimit{Requests: 2}
	for i := range memoryRateLimitCapacity {
		if allowed, _, err := store.Allow(ctx, fmt.Sprint(i), limit, 0); !allowed || err != nil {
			t.Fatalf("fill %d: allowed=%v err=%v", i, allowed, err)
		}
	}
	if allowed, _, err := store.Allow(ctx, "overflow", limit, 0); allowed || err == nil {
		t.Fatalf("full store must reject new identity: allowed=%v err=%v", allowed, err)
	}
	now = now.Add(30 * time.Second)
	if allowed, _, err := store.Allow(ctx, "0", limit, 0); !allowed || err != nil {
		t.Fatalf("existing identity must remain usable: allowed=%v err=%v", allowed, err)
	}
	if allowed, _, err := store.Allow(ctx, "0", limit, 0); allowed || err != nil {
		t.Fatalf("existing limit must remain enforced: allowed=%v err=%v", allowed, err)
	}
	if len(store.windows) != memoryRateLimitCapacity || store.expiry.Len() != memoryRateLimitCapacity {
		t.Fatal("capacity was exceeded")
	}
	now = now.Add(30 * time.Second)
	if allowed, _, err := store.Allow(ctx, "fresh", limit, 0); !allowed || err != nil {
		t.Fatalf("expired capacity not reclaimed: allowed=%v err=%v", allowed, err)
	}
	if len(store.windows) != 1 || store.expiry.Len() != 1 {
		t.Fatalf("expired identities retained: map=%d queue=%d", len(store.windows), store.expiry.Len())
	}
	now = now.Add(time.Hour)
	if allowed, _, err := store.Allow(ctx, "later", limit, 0); !allowed || err != nil {
		t.Fatal("hour-later request failed")
	}
	if len(store.windows) != 1 || store.expiry.Len() != 1 {
		t.Fatal("idle identities retained")
	}
}

func TestMemoryRateLimitRejectedRequestsDoNotAllocate(t *testing.T) {
	store := NewMemoryRateLimitStore()
	for i := range memoryRateLimitCapacity + 1 {
		if allowed, _, err := store.Allow(context.Background(), fmt.Sprint(i), RateLimit{Tokens: 1}, 2); allowed || err != nil {
			t.Fatalf("over-limit request: allowed=%v err=%v", allowed, err)
		}
	}
	if len(store.windows) != 0 || store.expiry.Len() != 0 {
		t.Fatal("rejected requests allocated windows")
	}
}

func TestMemoryRateLimitConcurrentAdmission(t *testing.T) {
	store := NewMemoryRateLimitStore()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	var admitted atomic.Int64
	var group sync.WaitGroup
	for range 100 {
		group.Add(1)
		go func() {
			defer group.Done()
			allowed, _, err := store.Allow(context.Background(), "shared", RateLimit{Tokens: 10}, 1)
			if err != nil {
				t.Error(err)
			}
			if allowed {
				admitted.Add(1)
			}
		}()
	}
	group.Wait()
	if admitted.Load() != 10 {
		t.Fatalf("admitted=%d, want 10", admitted.Load())
	}
}

func TestMemoryRateLimitCapacityReturnsUnavailable(t *testing.T) {
	store := NewMemoryRateLimitStore()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	for i := range memoryRateLimitCapacity {
		if allowed, _, err := store.Allow(context.Background(), fmt.Sprint(i), RateLimit{Requests: 1}, 0); !allowed || err != nil {
			t.Fatal("could not fill store")
		}
	}
	handler := Handler{rateLimits: store}
	response := httptest.NewRecorder()
	if handler.authorizeAccess(response, context.Background(), modules.RequestContext{CredentialID: "new", RateLimitRPM: 1}, "test", 0) {
		t.Fatal("full store admitted new identity")
	}
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "rate_limit_unavailable") {
		t.Fatalf("unexpected capacity error: %d %s", response.Code, response.Body.String())
	}
}
