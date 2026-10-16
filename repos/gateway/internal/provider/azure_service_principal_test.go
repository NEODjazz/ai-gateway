package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAzureServicePrincipalCredentialRefreshAndIsolation(t *testing.T) {
	const secret = azureServicePrincipalPrefix + `{"tenant_id":"tenant-a","client_id":"client-a","client_secret":"private-a"}`
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/tenant-a/oauth2/v2.0/token" {
			t.Errorf("unexpected token path: %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("client_id") != "client-a" || r.Form.Get("client_secret") != "private-a" || r.Form.Get("scope") != azureFoundryResource+".default" {
			t.Errorf("unexpected token request: client=%q scope=%q", r.Form.Get("client_id"), r.Form.Get("scope"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"issued-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer server.Close()
	source := newAzureTokenSourceWithPolicy(secret, "https://project.services.ai.azure.com/api/projects/example", "", "")
	source.authorityBaseURL = server.URL
	source.getenv = func(string) string { return "unrelated-environment-identity" }
	for range 2 {
		token, err := source.Token(context.Background())
		if err != nil || token != "issued-token" {
			t.Fatalf("token = %q, err = %v", token, err)
		}
	}
	if calls != 1 {
		t.Fatalf("token requests = %d, want 1", calls)
	}
	source.invalidate("issued-token")
	if _, err := source.Token(context.Background()); err != nil || calls != 2 {
		t.Fatalf("token refresh failed: calls=%d err=%v", calls, err)
	}
}

func TestAzureServicePrincipalCredentialValidation(t *testing.T) {
	const valid = azureServicePrincipalPrefix + `{"tenant_id":"tenant-a","client_id":"client-a","client_secret":"private-a"}`
	for _, secret := range []string{
		azureServicePrincipalPrefix + `{"tenant_id":"tenant-a","client_id":"client-a"}`,
		azureServicePrincipalPrefix + `{"tenant_id":"../unsafe","client_id":"client-a","client_secret":"private-a"}`,
		azureServicePrincipalPrefix + `{"tenant_id":"tenant-a","client_id":"client-a","client_secret":"private-a","extra":true}`,
		azureServicePrincipalPrefix + `{"tenant_id":"tenant-a","client_id":"client-a","client_secret":"private-a"} trailing`,
	} {
		if _, matched, err := parseAzureServicePrincipal(secret); !matched || err == nil || strings.Contains(err.Error(), "private-a") {
			t.Fatalf("invalid credential accepted or exposed: matched=%v err=%v", matched, err)
		}
	}
	if _, matched, err := parseAzureServicePrincipal("static-bearer-token"); matched || err != nil {
		t.Fatalf("static bearer changed: matched=%v err=%v", matched, err)
	}
	router := New(Config{CredentialEncryptionKey: []byte("test-encryption-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "azure", Type: "azure-openai", BaseURL: "https://example.openai.azure.com/openai/v1", AuthType: "entra", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateProvider(ManagedProvider{ID: "other", Type: "openai-compatible", BaseURL: "https://example.test", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []CredentialInput{{ID: "shared", Secret: valid}, {ID: "wrong", ProviderID: "other", Secret: valid}, {ID: "invalid", ProviderID: "azure", Secret: azureServicePrincipalPrefix + `{}`}} {
		if _, err := router.CreateCredential(input); !errors.Is(err, ErrInvalidCredential) {
			t.Fatalf("credential %s: want invalid, got %v", input.ID, err)
		}
	}
	created, err := router.CreateCredential(CredentialInput{ID: "sp", ProviderID: "azure", Secret: valid})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(created)
	if err != nil || created.Kind != "azure_service_principal" || strings.Contains(string(encoded), "private-a") {
		t.Fatalf("credential metadata did not identify service principal safely: kind=%q err=%v", created.Kind, err)
	}
	if _, err := router.UpdateCredential("sp", CredentialInput{ProviderID: "other"}); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("moving service principal to other provider: %v", err)
	}
	if _, err := router.RotateCredential("sp", azureServicePrincipalPrefix+`{}`); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("invalid rotation: %v", err)
	}
	if _, err := router.UpdateProvider("azure", ManagedProvider{Type: "azure-openai", BaseURL: "https://example.openai.azure.com/openai/v1", AuthType: "api_key", Enabled: true}); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("changing provider auth with service principal: %v", err)
	}
}

func TestAzureServicePrincipalRotationRebuildsRuntimeEndpoint(t *testing.T) {
	const first = azureServicePrincipalPrefix + `{"tenant_id":"tenant-a","client_id":"client-a","client_secret":"private-a"}`
	const replacement = azureServicePrincipalPrefix + `{"tenant_id":"tenant-b","client_id":"client-b","client_secret":"private-b"}`
	router := New(Config{CredentialEncryptionKey: []byte("test-encryption-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "azure", Type: "azure-openai", BaseURL: "https://example.openai.azure.com/openai/v1", AuthType: "entra", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "sp", ProviderID: "azure", Secret: first}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateModelDeployment(ModelDeployment{ID: "azure-chat", ProviderID: "azure", CredentialID: "sp", Models: []string{"public"}, Capabilities: []string{"chat"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	principal := func() *azureServicePrincipal {
		t.Helper()
		for _, endpoint := range router.runtimeEndpoints() {
			if endpoint.Name != "azure-chat" {
				continue
			}
			client, ok := endpoint.Provider.(OpenAICompatible)
			if !ok {
				t.Fatal("Azure deployment did not use OpenAI-compatible adapter")
			}
			transport, ok := client.client.Transport.(azureOpenAITransport)
			if !ok || transport.tokenSource.servicePrincipal == nil {
				t.Fatal("Azure deployment did not use service principal token source")
			}
			return transport.tokenSource.servicePrincipal
		}
		t.Fatal("Azure deployment is missing")
		return nil
	}
	if principal().ClientID != "client-a" {
		t.Fatal("initial service principal was not selected")
	}
	rotated, err := router.RotateCredential("sp", replacement)
	if err != nil || rotated.Kind != "azure_service_principal" || principal().ClientID != "client-b" {
		t.Fatalf("service principal rotation did not reach active deployment: err=%v kind=%q", err, rotated.Kind)
	}
}
