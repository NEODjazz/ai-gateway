package provider

import (
	"context"
	"errors"
	"testing"
)

func TestModelGroupRoutingUpdateIsAtomicAndRevisionGuarded(t *testing.T) {
	store := &memoryControlPlaneStore{}
	runtime, err := NewWithError(Config{CredentialEncryptionKey: []byte("routing-settings-test-key"), ControlPlaneStore: store})
	if err != nil {
		t.Fatal(err)
	}
	router := runtime.(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "managed", Type: "demo", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, deployment := range []ModelDeployment{
		{ID: "primary", ProviderID: "managed", UpstreamModel: "upstream-a", Models: []string{"public"}, Priority: 0, Weight: 3, Enabled: true},
		{ID: "fallback", ProviderID: "managed", UpstreamModel: "upstream-b", Models: []string{"public"}, Priority: 1, Weight: 1, Enabled: true},
	} {
		if _, err := router.CreateModelDeployment(deployment); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := router.CreateModelGroup(ModelGroup{ID: "public", DeploymentIDs: []string{"primary", "fallback"}, Strategy: "weighted", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	before, err := router.GetModelGroupRouting(context.Background(), "public")
	if err != nil {
		t.Fatal(err)
	}
	result, err := router.UpdateModelGroupRouting(context.Background(), "public", ModelGroupRoutingUpdate{
		ExpectedRevision: before.Revision,
		ModelGroupRoutingInput: ModelGroupRoutingInput{
			DeploymentIDs: []string{"fallback", "primary"}, Strategy: "adaptive", RetryPolicy: map[string]int{"timeout": 2}, Enabled: true,
			Deployments: []DeploymentRoutingSettings{
				{ID: "fallback", Priority: 0, Weight: 2, RequestTimeoutMS: 2500, MaxRetries: 1},
				{ID: "primary", Priority: 1, Weight: 4, RequestTimeoutMS: 1000, CooldownAfterFailures: 2, CooldownSeconds: 30},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != before.Revision+1 || result.ModelGroup.Strategy != "adaptive" || result.ModelGroup.DeploymentIDs[0] != "fallback" || result.Deployments[0].Priority != 0 || result.Deployments[1].Weight != 4 {
		t.Fatalf("unexpected atomic routing result: %+v", result)
	}
	if _, err := router.UpdateModelGroupRouting(context.Background(), "public", ModelGroupRoutingUpdate{ExpectedRevision: before.Revision, ModelGroupRoutingInput: resultInput(result)}); !errors.Is(err, ErrControlPlaneConflict) {
		t.Fatalf("stale routing update returned %v", err)
	}

	invalid := resultInput(result)
	invalid.Deployments[0].Priority = 9
	invalid.Deployments[1].Priority = -1
	if _, err := router.UpdateModelGroupRouting(context.Background(), "public", ModelGroupRoutingUpdate{ExpectedRevision: result.Revision, ModelGroupRoutingInput: invalid}); !errors.Is(err, ErrInvalidDeployment) {
		t.Fatalf("invalid routing update returned %v", err)
	}
	after, err := router.GetModelGroupRouting(context.Background(), "public")
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != result.Revision || after.Deployments[0].Priority != 0 || after.Deployments[1].Priority != 1 {
		t.Fatalf("failed atomic update leaked partial state: %+v", after)
	}
	persistenceFailure := resultInput(after)
	persistenceFailure.Deployments[0].Priority = 7
	store.mu.Lock()
	store.saveErr = errors.New("postgres unavailable")
	store.mu.Unlock()
	if _, err := router.UpdateModelGroupRouting(context.Background(), "public", ModelGroupRoutingUpdate{ExpectedRevision: after.Revision, ModelGroupRoutingInput: persistenceFailure}); err == nil {
		t.Fatal("expected routing persistence failure")
	}
	rolledBack, err := router.GetModelGroupRouting(context.Background(), "public")
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Revision != after.Revision || rolledBack.Deployments[0].Priority != 0 {
		t.Fatalf("persistence failure leaked routing state: %+v", rolledBack)
	}
}

func resultInput(settings ModelGroupRoutingSettings) ModelGroupRoutingInput {
	result := ModelGroupRoutingInput{DeploymentIDs: append([]string(nil), settings.ModelGroup.DeploymentIDs...), Strategy: settings.ModelGroup.Strategy, RetryPolicy: cloneRetryPolicy(settings.ModelGroup.RetryPolicy), Enabled: settings.ModelGroup.Enabled}
	for _, deployment := range settings.Deployments {
		result.Deployments = append(result.Deployments, DeploymentRoutingSettings{ID: deployment.ID, Priority: deployment.Priority, Weight: deployment.Weight, RequestTimeoutMS: deployment.RequestTimeoutMS, MaxRetries: deployment.MaxRetries, CooldownAfterFailures: deployment.CooldownAfterFailures, CooldownSeconds: deployment.CooldownSeconds, MaxParallelRequests: deployment.MaxParallelRequests, QueueCapacity: deployment.QueueCapacity, QueueTimeoutMS: deployment.QueueTimeoutMS})
	}
	return result
}
