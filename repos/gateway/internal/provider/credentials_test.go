package provider

import (
	"context"
	"errors"
	"testing"
)

func TestCredentialMetadataUpdatePreservesSecretAndProviderBoundary(t *testing.T) {
	runtime := New(Config{CredentialEncryptionKey: []byte("credential-lifecycle-key")})
	router := runtime.(*Router)
	for _, item := range []ManagedProvider{{ID: "first", Type: "demo", Enabled: true}, {ID: "second", Type: "demo", Enabled: true}} {
		if _, err := router.CreateProvider(item); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "first-key", ProviderID: "first", Description: "initial", Secret: "original-secret"}); err != nil {
		t.Fatal(err)
	}
	updated, err := router.UpdateCredential("first-key", CredentialInput{ProviderID: "first", Description: "metadata only"})
	if err != nil || updated.Description != "metadata only" {
		t.Fatalf("metadata update failed: credential=%+v err=%v", updated, err)
	}
	if secret, err := router.credentialSecret("first-key"); err != nil || secret != "original-secret" {
		t.Fatalf("metadata update changed secret: secret=%q err=%v", secret, err)
	}
	if _, err := router.CreateModelDeployment(ModelDeployment{ID: "wrong-provider", ProviderID: "second", CredentialID: "first-key", Models: []string{"model"}, Weight: 1, Enabled: true}); !errors.Is(err, ErrInvalidDeployment) {
		t.Fatalf("deployment accepted credential bound to another provider: %v", err)
	}
	if _, err := router.CreateModelDeployment(ModelDeployment{ID: "first-deployment", ProviderID: "first", CredentialID: "first-key", Models: []string{"model"}, Weight: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.UpdateCredential("first-key", CredentialInput{ProviderID: "second", Description: "unsafe rebind"}); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("credential used by another provider was rebound: %v", err)
	}
	if _, err := router.UpdateCredential("first-key", CredentialInput{Description: "shared"}); err != nil {
		t.Fatalf("credential could not be made shared: %v", err)
	}
	if _, err := router.RotateCredential("first-key", "rotated-secret"); err != nil {
		t.Fatal(err)
	}
	if secret, err := router.credentialSecret("first-key"); err != nil || secret != "rotated-secret" {
		t.Fatalf("rotation did not replace secret: secret=%q err=%v", secret, err)
	}
	rotated := router.ListCredentials(context.Background())[0]
	if rotated.ProviderID != "" || rotated.Description != "shared" {
		t.Fatalf("rotation changed metadata: %+v", rotated)
	}
}

func TestCreateCredentialStillRequiresSecret(t *testing.T) {
	router := New(Config{CredentialEncryptionKey: []byte("credential-lifecycle-key")}).(*Router)
	if _, err := router.CreateCredential(CredentialInput{ID: "empty"}); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("credential without secret was created: %v", err)
	}
}
