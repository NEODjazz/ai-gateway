package provider

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEndpointHealthCooldownAndRecovery(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	health := newEndpointHealthTracker()
	health.now = func() time.Time { return now }
	endpoint := Endpoint{Name: "primary", CooldownAfterFailures: 2, Cooldown: time.Minute}
	ctx := context.Background()

	health.failure(ctx, endpoint, statusError("primary", 503))
	if !health.available(ctx, endpoint) {
		t.Fatal("endpoint cooled down before reaching threshold")
	}
	health.failure(ctx, endpoint, statusError("primary", 503))
	if health.available(ctx, endpoint) {
		t.Fatal("expected endpoint to be cooling down")
	}
	now = now.Add(time.Minute)
	if !health.available(ctx, endpoint) {
		t.Fatal("expected endpoint to recover after cooldown")
	}
	if err := health.permit(ctx, endpoint); err != nil {
		t.Fatalf("expected one half-open probe: %v", err)
	}
	if err := health.permit(ctx, endpoint); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("second half-open probe was allowed: %v", err)
	}
	health.failure(ctx, endpoint, statusError("primary", 503))
	if health.available(ctx, endpoint) {
		t.Fatal("failed half-open probe did not reopen circuit")
	}
	now = now.Add(time.Minute)
	if err := health.permit(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	health.success(ctx, endpoint)
	if err := health.permit(ctx, endpoint); err != nil {
		t.Fatalf("success did not close circuit: %v", err)
	}
}

func TestFailurePolicy(t *testing.T) {
	if !retrySameEndpoint(statusError("provider", 503)) || !tryNextEndpoint(statusError("provider", 503)) {
		t.Fatal("503 must be retryable and allow failover")
	}
	if retrySameEndpoint(statusError("provider", 429)) || !tryNextEndpoint(statusError("provider", 429)) {
		t.Fatal("429 must skip same-endpoint retry and allow failover")
	}
	if tryNextEndpoint(statusError("provider", 400)) || shouldCooldown(statusError("provider", 400)) {
		t.Fatal("400 must be terminal and must not affect endpoint health")
	}
	if shouldCooldown(context.Canceled) {
		t.Fatal("client cancellation must not affect shared endpoint health")
	}
}
