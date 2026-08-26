package provider

import (
	"context"
	"testing"

	"ai-gateway-gateway/internal/config"
)

func TestRoutingSimulationDoesNotAdvanceLiveWeightedCounter(t *testing.T) {
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{
		{Name: "a", Type: "demo", Models: []string{"model"}, Priority: 1, Weight: 1},
		{Name: "b", Type: "demo", Models: []string{"model"}, Priority: 1, Weight: 1},
	}}).(*Router)
	before := router.routeCounter.Load()
	result := router.SimulateRouting(context.Background(), RoutingSimulationRequest{Model: "model"})
	if result.Selected == "" || len(result.Candidates) != 2 || result.Candidates[0].Order != 1 {
		t.Fatalf("unexpected simulation: %+v", result)
	}
	if after := router.routeCounter.Load(); after != before {
		t.Fatalf("simulation changed live routing state: before=%d after=%d", before, after)
	}
}
