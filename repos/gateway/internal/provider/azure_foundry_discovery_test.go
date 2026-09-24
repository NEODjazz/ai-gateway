package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestAzureFoundryProjectDiscoveryPaginatesDeployments(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/projects/project-a/deployments" || r.URL.Query().Get("api-version") != "v1" || r.Header.Get("Authorization") != "Bearer foundry-token" || r.Header.Get("api-key") != "" {
			t.Errorf("unexpected discovery request: %s headers=%v", r.URL.String(), r.Header)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		switch r.URL.Query().Get("page") {
		case "":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value":    []any{map[string]any{"name": "z-model", "type": "ModelDeployment"}},
				"nextLink": "?page=2",
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
	if err != nil || len(models) != 2 || models[0].ID != "a-model" || models[1].ID != "z-model" || calls.Load() != 2 {
		t.Fatalf("models=%+v calls=%d err=%v", models, calls.Load(), err)
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
