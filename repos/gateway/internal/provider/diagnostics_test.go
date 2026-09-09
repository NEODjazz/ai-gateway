package provider

import (
	"context"
	"testing"
	"time"
)

func TestRoutingDiagnosticsExposeSafeOperationalState(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	health := newEndpointHealthTracker()
	health.now = func() time.Time { return now }
	endpoint := Endpoint{
		Name: "primary", Type: "ollama", Models: []string{"m1"}, Priority: 1, Weight: 2,
		CooldownAfterFailures: 1, Cooldown: time.Minute, Admission: newAdmissionController(2, 3, time.Second),
		Capabilities: []string{"chat", "stream"}, GuardrailPolicy: "strict", GuardrailPolicyValid: true, DLPEnabled: true,
		RateLimitRPM: 120, RateLimitTPM: 64000,
		ProviderRateLimitRPM: 500, ProviderRateLimitTPM: 250000,
	}
	health.failure(context.Background(), endpoint, statusError("primary", 503))
	adaptive := newAdaptiveRouter(0.5)
	adaptive.observe("primary", 20*time.Millisecond, statusError("primary", 503))
	router := Router{routingStrategy: "adaptive", endpoints: []Endpoint{endpoint}, health: health, adaptive: adaptive}
	diagnostics := router.Diagnostics(context.Background())
	if diagnostics.Strategy != "adaptive" || len(diagnostics.Endpoints) != 1 {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
	got := diagnostics.Endpoints[0]
	if got.State != "cooling_down" || got.CooldownUntil == nil || got.Samples != 1 || got.LatencyEWMAms != 20 || got.FailureEWMA != 1 || got.MaxParallelRequests != 2 || got.QueueCapacity != 3 || got.RateLimitRPM != 120 || got.RateLimitTPM != 64000 || got.ProviderRateLimitRPM != 500 || got.ProviderRateLimitTPM != 250000 || !got.DLPEnabled {
		t.Fatalf("endpoint diagnostics=%+v", got)
	}
}
