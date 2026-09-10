package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestManagedBedrockDiscoveryUsesNativeContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/proxy/foundation-models" || r.Header.Get("Authorization") != "Bearer bedrock-key" {
			t.Errorf("unexpected discovery request: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = fmt.Fprint(w, `{"modelSummaries":[{"modelId":"amazon.nova-lite-v1:0"},{"modelId":"anthropic.claude-sonnet-4-20250514-v1:0"}]}`)
	}))
	defer server.Close()

	router := New(Config{CredentialEncryptionKey: []byte("bedrock-discovery-key")}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "bedrock", Type: "bedrock", BaseURL: server.URL + "/proxy", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.CreateCredential(CredentialInput{ID: "bedrock-key", ProviderID: "bedrock", Secret: "bedrock-key"}); err != nil {
		t.Fatal(err)
	}
	models, err := router.DiscoverProviderModels(t.Context(), "bedrock", "bedrock-key")
	if err != nil || len(models) != 2 || models[0].ID != "amazon.nova-lite-v1:0" || models[1].ID != "anthropic.claude-sonnet-4-20250514-v1:0" {
		t.Fatalf("models=%v err=%v", models, err)
	}
}

func TestBedrockDiscoveryURLUsesControlPlaneHost(t *testing.T) {
	got, err := discoveryURL(ManagedProvider{Type: "bedrock", BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://bedrock.us-east-1.amazonaws.com/foundation-models" {
		t.Fatalf("discovery URL=%q", got)
	}
}

func TestParseBedrockDiscoveryBoundsAndSortsModels(t *testing.T) {
	models, err := parseDiscoveredModels("bedrock", []byte(`{"modelSummaries":[{"modelId":"model-b"},{"modelId":""},{"modelId":"model-a"},{"modelId":"model-b"}]}`))
	if err != nil || len(models) != 2 || models[0].ID != "model-a" || models[1].ID != "model-b" {
		t.Fatalf("models=%v err=%v", models, err)
	}
}

func TestParseBedrockDiscoveryRejectsMissingCatalog(t *testing.T) {
	if _, err := parseDiscoveredModels("bedrock", []byte(`{}`)); err == nil {
		t.Fatal("missing modelSummaries was accepted")
	}
}
