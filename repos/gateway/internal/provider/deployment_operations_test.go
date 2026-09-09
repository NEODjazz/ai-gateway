package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"ai-gateway-gateway/internal/openai"
)

type deadlineProbeClient struct{}

func (deadlineProbeClient) ChatCompletions(ctx context.Context, _ openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	<-ctx.Done()
	return openai.ChatCompletionResponse{}, ctx.Err()
}

func (deadlineProbeClient) Responses(ctx context.Context, _ openai.ResponseRequest) (openai.ResponseResponse, error) {
	<-ctx.Done()
	return openai.ResponseResponse{}, ctx.Err()
}

func TestManagedDeploymentAppliesOperationalSettings(t *testing.T) {
	router := New(Config{}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "demo-managed", Type: "demo", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	input := ModelDeployment{ID: "managed", ProviderID: "demo-managed", Models: []string{"model"}, Weight: 1, RequestTimeoutMS: 2500, MaxRetries: 3, CooldownAfterFailures: 4, CooldownSeconds: 30, MaxParallelRequests: 5, QueueCapacity: 7, QueueTimeoutMS: 900, RateLimitRPM: 120, RateLimitTPM: 64000, Enabled: true}
	if _, err := router.CreateModelDeployment(input); err != nil {
		t.Fatal(err)
	}
	var endpoint Endpoint
	for _, candidate := range router.configuredEndpoints() {
		if candidate.Name == "managed" {
			endpoint = candidate
		}
	}
	if endpoint.RequestTimeout != 2500*time.Millisecond || endpoint.MaxRetries != 3 || endpoint.CooldownAfterFailures != 4 || endpoint.Cooldown != 30*time.Second || endpoint.Admission == nil || cap(endpoint.Admission.slots) != 5 || endpoint.Admission.queueCapacity != 7 || endpoint.Admission.queueTimeout != 900*time.Millisecond || endpoint.RateLimitRPM != 120 || endpoint.RateLimitTPM != 64000 {
		t.Fatalf("operational settings were not applied: %+v admission=%+v", endpoint, endpoint.Admission)
	}
	invalid := input
	invalid.ID = "invalid"
	invalid.MaxParallelRequests = 0
	if _, err := router.CreateModelDeployment(invalid); !errors.Is(err, ErrInvalidDeployment) {
		t.Fatalf("expected invalid admission settings, got %v", err)
	}
	invalid = input
	invalid.ID = "invalid-quota"
	invalid.RateLimitTPM = 1000000001
	if _, err := router.CreateModelDeployment(invalid); !errors.Is(err, ErrInvalidDeployment) {
		t.Fatalf("expected invalid quota settings, got %v", err)
	}
}

func TestProviderCallHonorsDeploymentRequestTimeout(t *testing.T) {
	router := Router{health: newEndpointHealthTracker(), adaptive: newAdaptiveRouter(0.5)}
	endpoint := Endpoint{Name: "slow", Type: "test", Provider: deadlineProbeClient{}, RequestTimeout: 10 * time.Millisecond}
	started := time.Now()
	_, _, err := router.callChat(context.Background(), endpoint, openai.ChatCompletionRequest{Model: "model"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("request timeout was not applied: %s", elapsed)
	}
}
