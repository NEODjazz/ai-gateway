package provider

import (
	"errors"
	"testing"
)

func TestModelGroupFallbackGraphValidationAndLifecycle(t *testing.T) {
	store := &memoryControlPlaneStore{}
	config := Config{CredentialEncryptionKey: []byte("model-group-fallback-test-key"), ControlPlaneStore: store}
	runtime, err := NewWithError(config)
	if err != nil {
		t.Fatal(err)
	}
	router := runtime.(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "managed", Type: "demo", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, deployment := range []ModelDeployment{
		{ID: "primary-deployment", ProviderID: "managed", UpstreamModel: "primary-upstream", Models: []string{"primary"}, Weight: 1, Enabled: true},
		{ID: "general-deployment", ProviderID: "managed", UpstreamModel: "general-upstream", Models: []string{"general"}, Weight: 1, Enabled: true},
		{ID: "context-deployment", ProviderID: "managed", UpstreamModel: "context-upstream", Models: []string{"context"}, Weight: 1, Enabled: true},
	} {
		if _, err := router.CreateModelDeployment(deployment); err != nil {
			t.Fatal(err)
		}
	}
	for _, group := range []ModelGroup{
		{ID: "general", DeploymentIDs: []string{"general-deployment"}, Strategy: "weighted", Enabled: true},
		{ID: "context", DeploymentIDs: []string{"context-deployment"}, Strategy: "weighted", Enabled: true},
		{ID: "primary", DeploymentIDs: []string{"primary-deployment"}, Strategy: "weighted", Fallbacks: map[string][]string{FallbackGeneral: {"general"}, FallbackContextWindow: {"context"}}, Enabled: true},
	} {
		if _, err := router.CreateModelGroup(group); err != nil {
			t.Fatalf("create group %s: %v", group.ID, err)
		}
	}
	groups := router.ListModelGroups(t.Context())
	if len(groups) != 3 || groups[2].Fallbacks[FallbackGeneral][0] != "general" {
		t.Fatalf("fallback graph missing from list: %+v", groups)
	}
	groups[2].Fallbacks[FallbackGeneral][0] = "mutated"
	if got := router.ListModelGroups(t.Context())[2].Fallbacks[FallbackGeneral][0]; got != "general" {
		t.Fatalf("list leaked mutable fallback graph: %q", got)
	}
	replicaRuntime, err := NewWithError(config)
	if err != nil {
		t.Fatal(err)
	}
	if got := replicaRuntime.(*Router).ListModelGroups(t.Context())[2].Fallbacks[FallbackContextWindow][0]; got != "context" {
		t.Fatalf("fallback graph was not restored: %q", got)
	}
	if _, err := router.UpdateModelGroup("general", ModelGroup{DeploymentIDs: []string{"general-deployment"}, Strategy: "weighted", Fallbacks: map[string][]string{FallbackGeneral: {"primary"}}, Enabled: true}); !errors.Is(err, ErrInvalidModelGroup) {
		t.Fatalf("cycle returned %v", err)
	}
	if _, err := router.UpdateModelGroup("primary", ModelGroup{DeploymentIDs: []string{"primary-deployment"}, Strategy: "weighted", Fallbacks: map[string][]string{FallbackGeneral: {"missing"}}, Enabled: true}); !errors.Is(err, ErrInvalidModelGroup) {
		t.Fatalf("unknown fallback target returned %v", err)
	}
	if err := router.DeleteModelGroup("general"); !errors.Is(err, ErrModelGroupInUse) {
		t.Fatalf("deleting referenced fallback group returned %v", err)
	}
}
