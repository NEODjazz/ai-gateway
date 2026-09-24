package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAzureFoundryProjectDiscoveryUsesExplicitGovernmentIdentity(t *testing.T) {
	for _, name := range []string{"AZURE_TENANT_ID", "AZURE_CLIENT_ID", "AZURE_FEDERATED_TOKEN_FILE"} {
		t.Setenv(name, "")
	}
	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("resource") != azureGovernmentFoundryResource || r.Header.Get("X-IDENTITY-HEADER") != "test-header" {
			t.Errorf("government identity request: query=%v headers=%v", r.URL.Query(), r.Header)
		}
		_, _ = fmt.Fprintf(w, `{"access_token":"government-project-token","expires_on":%d,"token_type":"Bearer"}`, time.Now().Add(time.Hour).Unix())
	}))
	t.Cleanup(identity.Close)
	t.Setenv("IDENTITY_ENDPOINT", identity.URL)
	t.Setenv("IDENTITY_HEADER", "test-header")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects/project-a/deployments" || r.URL.Query().Get("api-version") != "v1" || r.URL.Query().Get("deploymentType") != "ModelDeployment" || r.Header.Get("Authorization") != "Bearer government-project-token" {
			t.Errorf("government discovery request: %s headers=%v", r.URL.String(), r.Header)
			http.Error(w, "invalid discovery request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"name": "model-a", "type": "ModelDeployment"}}})
	}))
	t.Cleanup(server.Close)
	router := New(Config{}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "foundry-gov", Type: "azure-openai", BaseURL: server.URL + "/api/projects/project-a", AuthType: "entra", AzureCloud: "usgov", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(t.Context(), "foundry-gov", "")
	if err != nil || len(models) != 1 || models[0].ID != "model-a" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
}

func TestAzureFoundryProjectDiscoveryUsesConfiguredAudience(t *testing.T) {
	for _, name := range []string{"AZURE_TENANT_ID", "AZURE_CLIENT_ID", "AZURE_FEDERATED_TOKEN_FILE"} {
		t.Setenv(name, "")
	}
	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("resource") != azureGovernmentResource {
			t.Errorf("resource=%q", r.URL.Query().Get("resource"))
		}
		_, _ = fmt.Fprintf(w, `{"access_token":"cognitive-token","expires_on":%d,"token_type":"Bearer"}`, time.Now().Add(time.Hour).Unix())
	}))
	t.Cleanup(identity.Close)
	t.Setenv("IDENTITY_ENDPOINT", identity.URL)
	t.Setenv("IDENTITY_HEADER", "test-header")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects/project-a/deployments" || r.Header.Get("Authorization") != "Bearer cognitive-token" {
			t.Errorf("discovery request=%s authorization=%q", r.URL, r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"name": "model-a", "type": "ModelDeployment"}}})
	}))
	t.Cleanup(server.Close)
	router := New(Config{}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "foundry-gov", Type: "azure-openai", BaseURL: server.URL + "/api/projects/project-a", AuthType: "entra", AzureCloud: "usgov", AzureAudience: "cognitive", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(t.Context(), "foundry-gov", "")
	if err != nil || len(models) != 1 || models[0].ID != "model-a" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
}

func TestAzureFoundryProjectDiscoveryPaginatesDeployments(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/projects/project-a/deployments" || r.URL.Query().Get("api-version") != "v1" || r.URL.Query().Get("deploymentType") != "ModelDeployment" || r.Header.Get("Authorization") != "Bearer foundry-token" || r.Header.Get("api-key") != "" {
			t.Errorf("unexpected discovery request: %s headers=%v", r.URL.String(), r.Header)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		switch r.URL.Query().Get("page") {
		case "":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": []any{
					map[string]any{"name": "z-model", "type": "ModelDeployment", "modelName": "embedding-base", "modelPublisher": "contoso"},
					map[string]any{"name": "non-model", "type": "ServerlessEndpoint"},
				},
				"nextLink": "?page=2&deploymentType=ServerlessEndpoint",
			})
		case "2":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": []any{map[string]any{"name": "a-model", "type": "ModelDeployment"}, map[string]any{"name": "z-model", "type": "ModelDeployment"}},
			})
		default:
			http.Error(w, "unexpected page", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	router := New(Config{CredentialEncryptionKey: []byte("foundry-discovery-test-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "foundry", Type: "azure-openai", BaseURL: server.URL + "/api/projects/project-a/openai/v1", AuthType: "entra", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "foundry-token", ProviderID: "foundry", Secret: "foundry-token"}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(t.Context(), "foundry", "foundry-token")
	if err != nil || len(models) != 2 || models[0].ID != "a-model" || models[1].ID != "z-model" || models[1].ModelName != "embedding-base" || models[1].Publisher != "contoso" || calls.Load() != 2 {
		t.Fatalf("models=%+v calls=%d err=%v", models, calls.Load(), err)
	}
}

func TestAzureFoundryProjectDiscoveryFiltersWithAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects/project-a/deployments" || r.URL.Query().Get("api-version") != "v1" || r.URL.Query().Get("deploymentType") != "ModelDeployment" || r.Header.Get("api-key") != "foundry-key" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected API-key discovery request: %s", r.URL)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"name": "model-a", "type": "ModelDeployment"}}})
	}))
	t.Cleanup(server.Close)
	router := New(Config{CredentialEncryptionKey: []byte("foundry-api-key-discovery-test")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "foundry", Type: "azure-openai", BaseURL: server.URL + "/api/projects/project-a", AuthType: "api_key", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "foundry-key", ProviderID: "foundry", Secret: "foundry-key"}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(t.Context(), "foundry", "foundry-key")
	if err != nil || len(models) != 1 || models[0].ID != "model-a" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
}

func TestAzureFoundryProjectDiscoveryRejectsOversizedModelIdentity(t *testing.T) {
	for _, field := range []string{"modelName", "modelPublisher"} {
		t.Run(field, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				item := map[string]any{"name": "model-a", "type": "ModelDeployment", field: strings.Repeat("x", 257)}
				_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{item}})
			}))
			t.Cleanup(server.Close)
			router := New(Config{CredentialEncryptionKey: []byte("foundry-model-metadata-test")}).(*Router)
			if _, err := router.CreateProvider(ManagedProvider{ID: "foundry", Type: "azure-openai", BaseURL: server.URL + "/api/projects/project-a", AuthType: "api_key", Enabled: true}); err != nil {
				t.Fatal(err)
			}
			if _, err := router.CreateCredential(CredentialInput{ID: "foundry-key", ProviderID: "foundry", Secret: "foundry-key"}); err != nil {
				t.Fatal(err)
			}
			if _, err := router.DiscoverProviderModels(t.Context(), "foundry", "foundry-key"); !errors.Is(err, ErrProviderProbeFailed) {
				t.Fatalf("oversized %s was accepted: %v", field, err)
			}
		})
	}
}

func TestAzureFoundryPrefixedProjectDiscoveryKeepsAuthAndPath(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/tenant/api/projects/project-a/deployments" || r.URL.Query().Get("api-version") != "v1" || r.Header.Get("Authorization") != "Bearer foundry-token" || r.Header.Get("api-key") != "" {
			t.Errorf("unexpected prefixed discovery request: %s headers=%v", r.URL, r.Header)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"name": "model-a", "type": "ModelDeployment"}}})
	}))
	t.Cleanup(server.Close)
	router := New(Config{CredentialEncryptionKey: []byte("prefixed-foundry-discovery-test-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "foundry", Type: "azure-openai", BaseURL: server.URL + "/tenant/api/projects/project-a/openai/v1", AuthType: "entra", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "foundry-token", ProviderID: "foundry", Secret: "foundry-token"}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(t.Context(), "foundry", "foundry-token")
	if err != nil || len(models) != 1 || models[0].ID != "model-a" || calls.Load() != 1 {
		t.Fatalf("models=%+v calls=%d err=%v", models, calls.Load(), err)
	}
}

func TestAzureFoundryPrefixedProjectDiscoveryRejectsAnotherPrefix(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"value":    []any{map[string]any{"name": "model-a", "type": "ModelDeployment"}},
			"nextLink": "/another-tenant/api/projects/project-a/deployments?api-version=v1",
		})
	}))
	t.Cleanup(server.Close)
	router := New(Config{CredentialEncryptionKey: []byte("prefixed-foundry-discovery-test-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "foundry", Type: "azure-openai", BaseURL: server.URL + "/tenant/api/projects/project-a", AuthType: "entra", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "foundry-token", ProviderID: "foundry", Secret: "foundry-token"}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.DiscoverProviderModels(t.Context(), "foundry", "foundry-token"); err == nil {
		t.Fatal("cross-prefix continuation was accepted")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("cross-prefix continuation made %d requests", got)
	}
}

func TestAzureFoundryProjectDiscoveryRejectsCrossOriginNextLink(t *testing.T) {
	var forwarded atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { forwarded.Store(true) }))
	t.Cleanup(target.Close)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"value":    []any{map[string]any{"name": "model-a", "type": "ModelDeployment"}},
			"nextLink": target.URL + "/api/projects/project-a/deployments?api-version=v1",
		})
	}))
	t.Cleanup(source.Close)
	router := New(Config{CredentialEncryptionKey: []byte("foundry-discovery-test-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "foundry", Type: "azure-openai", BaseURL: source.URL + "/api/projects/project-a", AuthType: "entra", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "foundry-token", ProviderID: "foundry", Secret: "foundry-token"}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.DiscoverProviderModels(t.Context(), "foundry", "foundry-token"); err == nil {
		t.Fatal("cross-origin continuation was accepted")
	}
	if forwarded.Load() {
		t.Fatal("credential-bearing request reached the other origin")
	}
}

func TestAzureFoundryProjectDiscoveryRejectsCrossProjectNextLink(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"value":    []any{map[string]any{"name": "model-a", "type": "ModelDeployment"}},
			"nextLink": "/api/projects/project-b/deployments?api-version=v1",
		})
	}))
	t.Cleanup(server.Close)
	router := New(Config{CredentialEncryptionKey: []byte("foundry-discovery-test-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "foundry", Type: "azure-openai", BaseURL: server.URL + "/api/projects/project-a", AuthType: "entra", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "foundry-token", ProviderID: "foundry", Secret: "foundry-token"}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.DiscoverProviderModels(t.Context(), "foundry", "foundry-token"); err == nil {
		t.Fatal("cross-project continuation was accepted")
	}
	if requests.Load() != 1 {
		t.Fatalf("discovery requests=%d", requests.Load())
	}
}
