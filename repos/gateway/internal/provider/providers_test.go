package provider

import (
	"context"
	"errors"
	"slices"
	"testing"

	"ai-gateway-gateway/internal/config"
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

func TestManagedGeminiWorkloadAuthentication(t *testing.T) {
	router := New(Config{}).(*Router)
	provider, err := router.CreateProvider(ManagedProvider{ID: "gemini", Type: "gemini", BaseURL: "https://generativelanguage.googleapis.com", AuthType: "gcp_adc", Enabled: true})
	if err != nil || provider.AuthType != "gcp_adc" {
		t.Fatalf("provider=%+v err=%v", provider, err)
	}
	if _, err := router.CreateProvider(ManagedProvider{ID: "invalid", Type: "gemini", BaseURL: "https://example.test", AuthType: "entra", Enabled: true}); !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("invalid auth type accepted: %v", err)
	}
	static := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "static", Type: "gemini", BaseURL: "https://example.test", AuthType: "gcp_adc", Models: []string{"model"}}}}).(*Router)
	providers := static.ListProviders(t.Context())
	if len(providers) != 1 || providers[0].AuthType != "gcp_adc" {
		t.Fatalf("static auth type lost: %+v", providers)
	}
}

func TestManagedVertexGeminiConfiguration(t *testing.T) {
	router := New(Config{}).(*Router)
	baseURL := "https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1/publishers/google"
	managed, err := router.CreateProvider(ManagedProvider{ID: "vertex", Type: "vertex-gemini", BaseURL: baseURL, Enabled: true})
	if err != nil || managed.AuthType != "gcp_adc" || managed.BaseURL != baseURL {
		t.Fatalf("provider=%+v err=%v", managed, err)
	}
	for _, invalid := range []ManagedProvider{
		{ID: "bad-auth", Type: "vertex-gemini", BaseURL: baseURL, AuthType: "api_key", Enabled: true},
		{ID: "bad-path", Type: "vertex-gemini", BaseURL: "https://us-central1-aiplatform.googleapis.com/v1", Enabled: true},
		{ID: "bad-host", Type: "vertex-gemini", BaseURL: "https://attacker.example/v1/projects/project-1/locations/us-central1/publishers/google", Enabled: true},
		{ID: "bad-location", Type: "vertex-gemini", BaseURL: "https://europe-west1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1/publishers/google", Enabled: true},
		{ID: "bad-query", Type: "vertex-gemini", BaseURL: baseURL + "?credential=secret", Enabled: true},
	} {
		if _, err := router.CreateProvider(invalid); !errors.Is(err, ErrInvalidProvider) {
			t.Fatalf("invalid provider accepted: %+v err=%v", invalid, err)
		}
	}
	for _, profile := range ManagedProviderCapabilityProfiles() {
		if profile.Type != "vertex-gemini" {
			continue
		}
		if len(profile.AuthTypes) != 1 || profile.AuthTypes[0] != "gcp_adc" || !slices.Equal(profile.Operations, []string{"chat", "count_tokens", "embeddings", "stream"}) {
			t.Fatalf("profile=%+v", profile)
		}
		return
	}
	t.Fatal("Vertex Gemini capability profile is missing")
}

func TestManagedVertexGeminiDeploymentRejectsBoundCredential(t *testing.T) {
	router := New(Config{CredentialEncryptionKey: []byte("vertex-credential-key")}).(*Router)
	baseURL := "https://us-central1-aiplatform.googleapis.com/v1/projects/project-1/locations/us-central1/publishers/google"
	if _, err := router.CreateProvider(ManagedProvider{ID: "vertex", Type: "vertex-gemini", BaseURL: baseURL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "unused", ProviderID: "vertex", Secret: "unused-secret"}); err != nil {
		t.Fatal(err)
	}
	_, err := router.CreateModelDeployment(ModelDeployment{ID: "vertex-model", ProviderID: "vertex", CredentialID: "unused", Models: []string{"vertex-model"}, UpstreamModel: "gemini-2.5-pro", Capabilities: []string{"chat"}, Enabled: true})
	if !errors.Is(err, ErrInvalidDeployment) {
		t.Fatalf("bound credential accepted: %v", err)
	}
}

func TestManagedOpenSandboxCapabilityAndAuthentication(t *testing.T) {
	router := New(Config{}).(*Router)
	managed, err := router.CreateProvider(ManagedProvider{ID: "sandbox", Type: "opensandbox", BaseURL: "https://sandbox.example", Enabled: true})
	if err != nil || managed.AuthType != "api_key" {
		t.Fatalf("provider=%+v err=%v", managed, err)
	}
	if _, err := router.CreateModelDeployment(ModelDeployment{ID: "sandbox-python", ProviderID: managed.ID, Models: []string{"code-interpreter"}, Capabilities: []string{"sandbox"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateProvider(ManagedProvider{ID: "invalid", Type: "opensandbox", BaseURL: "https://sandbox.example", AuthType: "bearer", Enabled: true}); !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("invalid auth type accepted: %v", err)
	}
	profiles := ManagedProviderCapabilityProfiles()
	found := false
	for _, profile := range profiles {
		if profile.Type == "opensandbox" {
			found = slicesContain(profile.Operations, "sandbox") && len(profile.AuthTypes) == 1 && profile.AuthTypes[0] == "api_key"
		}
	}
	if !found {
		t.Fatal("sandbox capability profile is missing")
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

func TestStaticBedrockSigV4SettingsReachManagedState(t *testing.T) {
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "aws", Type: "bedrock", BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com", AuthType: " AWS_SIGV4 ", Region: "US-EAST-1", Models: []string{"model"}, Capabilities: []string{"chat"}}}}).(*Router)
	providers := router.ListProviders(context.Background())
	if len(providers) != 1 || providers[0].AuthType != "aws_sigv4" || providers[0].Region != "us-east-1" {
		t.Fatalf("providers=%+v", providers)
	}
	snapshot := router.controlPlaneSnapshot()
	if len(snapshot.Providers) != 1 || snapshot.Providers[0].AuthType != "aws_sigv4" || snapshot.Providers[0].Region != "us-east-1" {
		t.Fatalf("snapshot providers=%+v", snapshot.Providers)
	}
}
