package provider

import (
	"context"
	"errors"
	"testing"
)

func TestProviderUpdateRejectsIncompatibleExistingDeploymentAtomically(t *testing.T) {
	router := New(Config{}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "managed", Type: "openai-compatible", BaseURL: "https://provider.example", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateModelDeployment(ModelDeployment{ID: "images", ProviderID: "managed", Models: []string{"image-model"}, Capabilities: []string{"image_generation"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	_, err := router.UpdateProvider("managed", ManagedProvider{Type: "voyage", BaseURL: "https://provider.example", Enabled: true})
	if !errors.Is(err, ErrUnsupportedProviderCapability) {
		t.Fatalf("incompatible provider update accepted: %v", err)
	}
	providers := router.ListProviders(context.Background())
	if len(providers) != 1 || providers[0].Type != "openai-compatible" {
		t.Fatalf("failed update changed control plane: %+v", providers)
	}
	endpoints := router.runtimeEndpoints()
	var runtimeType string
	for _, endpoint := range endpoints {
		if endpoint.Name == "images" {
			runtimeType = endpoint.Type
		}
	}
	if runtimeType != "openai-compatible" {
		t.Fatalf("failed update changed runtime endpoint: %+v", endpoints)
	}
}
