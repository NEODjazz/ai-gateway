package provider

import (
	"context"
	"testing"
	"time"
)

func TestExactCacheRejectsOversizedEntry(t *testing.T) {
	cache := newExactCacheWithLimit(time.Minute, 3)
	if err := cache.set(context.Background(), "key", []byte("four")); err != nil {
		t.Fatal(err)
	}
	if _, found, err := cache.get(context.Background(), "key"); err != nil || found {
		t.Fatalf("oversized response must not be cached: found=%v err=%v", found, err)
	}
}
