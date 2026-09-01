package gateway

import (
	"context"
	"testing"
	"time"

	"ai-gateway-gateway/internal/redisstore"
	"github.com/alicebob/miniredis/v2"
)

func TestRedisGuardrailEventStoreSharesOnlyEventsInsideRetentionWindow(t *testing.T) {
	server := miniredis.RunT(t)
	store := NewRedisGuardrailEventStore(redisstore.New(redisstore.Config{Addr: server.Addr(), Prefix: "guardrail-test"}), time.Hour)
	ctx := context.Background()
	if err := store.Append(ctx, GuardrailEvent{OccurredAt: time.Now().UTC().Add(-2 * time.Hour), RequestID: "expired", Module: "dlp", Source: "inference", Outcome: "passed"}, 10); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, GuardrailEvent{OccurredAt: time.Now().UTC(), RequestID: "current", Module: "av", Source: "compliance", Outcome: "rejected"}, 10); err != nil {
		t.Fatal(err)
	}
	events, err := store.Recent(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].RequestID != "current" {
		t.Fatalf("unexpected retained events: %+v", events)
	}
}
