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

func TestManagedBedrockSigV4ValidatesRegionAndVaultCredential(t *testing.T) {
	router := New(Config{CredentialEncryptionKey: []byte("bedrock-managed-test-key")}).(*Router)
	provider, err := router.CreateProvider(ManagedProvider{ID: "aws", Type: "bedrock", BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com", AuthType: "aws_sigv4", Region: "us-east-1", Enabled: true})
	if err != nil || provider.Region != "us-east-1" || provider.AuthType != "aws_sigv4" {
		t.Fatalf("provider=%+v err=%v", provider, err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "invalid", ProviderID: "aws", Secret: `{}`}); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("invalid credential error=%v", err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "valid", ProviderID: "aws", Secret: `{"access_key_id":"AKID","secret_access_key":"secret"}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateProvider(ManagedProvider{ID: "missing-region", Type: "bedrock", BaseURL: "https://example.test", AuthType: "aws_sigv4", Enabled: true}); !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("missing region error=%v", err)
	}
}

func TestManagedBedrockSigV4RejectsExistingBearerCredential(t *testing.T) {
	router := New(Config{CredentialEncryptionKey: []byte("bedrock-update-test-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "aws", Type: "bedrock", BaseURL: "https://private.example", AuthType: "bearer", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "bearer", ProviderID: "aws", Secret: "opaque-token"}); err != nil {
		t.Fatal(err)
	}
	_, err := router.UpdateProvider("aws", ManagedProvider{Type: "bedrock", BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com", AuthType: "aws_sigv4", Region: "us-east-1", Enabled: true})
	if !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("invalid existing credential accepted: %v", err)
	}
	providers := router.ListProviders(context.Background())
	if len(providers) != 1 || providers[0].AuthType != "bearer" {
		t.Fatalf("failed update changed provider: %+v", providers)
	}
}

func TestControlPlaneRejectsInvalidBedrockSigV4Credential(t *testing.T) {
	router := New(Config{CredentialEncryptionKey: []byte("bedrock-snapshot-test-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "aws", Type: "bedrock", BaseURL: "https://private.example", AuthType: "bearer", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "bearer", ProviderID: "aws", Secret: "opaque-token"}); err != nil {
		t.Fatal(err)
	}
	snapshot := router.controlPlaneSnapshot()
	snapshot.Providers[0].BaseURL = "https://bedrock-runtime.us-east-1.amazonaws.com"
	snapshot.Providers[0].AuthType = "aws_sigv4"
	snapshot.Providers[0].Region = "us-east-1"
	if err := router.applyControlPlaneSnapshot(snapshot); err == nil {
		t.Fatal("invalid persisted SigV4 credential accepted")
	}
}
