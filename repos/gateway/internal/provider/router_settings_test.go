package provider

import (
	"context"
	"net/http"
	"testing"

	"ai-gateway-gateway/internal/config"
)

func TestModelGroupRetryPolicyIsValidatedAndShownInSimulation(t *testing.T) {
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "primary", Type: "demo", Models: []string{"public"}}}}).(*Router)
	group, err := router.CreateModelGroup(ModelGroup{ID: "public", DeploymentIDs: []string{"primary"}, Strategy: "weighted", RetryPolicy: map[string]int{"rate_limit": 2, "timeout": 3}, Enabled: true})
	if err != nil || group.RetryPolicy["timeout"] != 3 {
		t.Fatalf("create group with retry policy: group=%+v err=%v", group, err)
	}
	result := router.SimulateRouting(context.Background(), RoutingSimulationRequest{Model: "public"})
	if len(result.Candidates) != 1 || result.Candidates[0].RetryPolicy["rate_limit"] != 2 {
		t.Fatalf("simulation does not expose effective retry policy: %+v", result)
	}
	if _, err := router.UpdateModelGroup("public", ModelGroup{DeploymentIDs: []string{"primary"}, Strategy: "weighted", RetryPolicy: map[string]int{"client_request": 1}, Enabled: true}); err != ErrInvalidModelGroup {
		t.Fatalf("expected invalid retry class, got %v", err)
	}
}

func TestEndpointRetryPolicyOverridesSafeFailureClasses(t *testing.T) {
	endpoint := Endpoint{MaxRetries: 1, RetryPolicy: map[string]int{"rate_limit": 3, "timeout": 0}}
	rateLimit := statusError("test", http.StatusTooManyRequests)
	if !retrySameEndpointWithPolicy(endpoint, rateLimit) || endpointRetryLimit(endpoint, rateLimit) != 3 || endpointMaxRetries(endpoint) != 3 {
		t.Fatalf("rate-limit retry override was not applied")
	}
	timeout := statusError("test", http.StatusGatewayTimeout)
	if retrySameEndpointWithPolicy(endpoint, timeout) || endpointRetryLimit(endpoint, timeout) != 0 {
		t.Fatalf("zero timeout retry override was not applied")
	}
	clientError := statusError("test", http.StatusBadRequest)
	if retrySameEndpointWithPolicy(endpoint, clientError) {
		t.Fatalf("client errors must never be retried")
	}
}
