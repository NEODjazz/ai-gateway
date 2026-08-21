package provider

import (
	"context"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
)

type testSessionStore struct {
	values  map[string][]byte
	lastTTL time.Duration
}

func (s *testSessionStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	value, found := s.values[key]
	return append([]byte(nil), value...), found, nil
}

func (s *testSessionStore) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	s.values[key] = append([]byte(nil), value...)
	s.lastTTL = ttl
	return nil
}

func TestExactCacheRejectsOversizedEntry(t *testing.T) {
	cache := newExactCacheWithLimit(time.Minute, 3)
	if err := cache.set(context.Background(), "key", []byte("four")); err != nil {
		t.Fatal(err)
	}
	if _, found, err := cache.get(context.Background(), "key"); err != nil || found {
		t.Fatalf("oversized response must not be cached: found=%v err=%v", found, err)
	}
}

func TestMemoryAffinityExpiresAndHashesTenantResponseID(t *testing.T) {
	store := newAffinityStore(time.Minute, nil).(*memoryAffinity)
	now := time.Unix(1_700_000_000, 0)
	store.now = func() time.Time { return now }
	req := modules.RequestContext{CredentialID: "credential-a"}
	key := affinityKey(req, "resp-secret")
	if key == "" || strings.Contains(key, "credential-a") || strings.Contains(key, "resp-secret") {
		t.Fatalf("affinity key leaks tenant or response id: %q", key)
	}
	if err := store.set(context.Background(), key, "endpoint-a"); err != nil {
		t.Fatal(err)
	}
	if endpoint, found, err := store.get(context.Background(), key); err != nil || !found || endpoint != "endpoint-a" {
		t.Fatalf("unexpected affinity value endpoint=%q found=%v err=%v", endpoint, found, err)
	}
	now = now.Add(time.Minute)
	if _, found, err := store.get(context.Background(), key); err != nil || found {
		t.Fatalf("expired affinity remained: found=%v err=%v", found, err)
	}
}

func TestDistributedAffinitySharesMappingAndTTL(t *testing.T) {
	shared := &testSessionStore{values: map[string][]byte{}}
	firstReplica := newAffinityStore(2*time.Hour, shared)
	secondReplica := newAffinityStore(2*time.Hour, shared)
	if err := firstReplica.set(context.Background(), "hashed-key", "endpoint-a"); err != nil {
		t.Fatal(err)
	}
	endpoint, found, err := secondReplica.get(context.Background(), "hashed-key")
	if err != nil || !found || endpoint != "endpoint-a" {
		t.Fatalf("mapping was not shared: endpoint=%q found=%v err=%v", endpoint, found, err)
	}
	if shared.lastTTL != 2*time.Hour {
		t.Fatalf("unexpected distributed ttl: %s", shared.lastTTL)
	}
}
