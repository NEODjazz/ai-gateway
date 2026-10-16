package provider

import (
	"crypto/rand"
	"os"
	"testing"
)

// The live suite is opt-in; normal tests never depend on a developer's daemon.
func TestLemonadeLiveDiscovery(t *testing.T) {
	baseURL := os.Getenv("LEMONADE_INTEGRATION_BASE_URL")
	model := os.Getenv("LEMONADE_INTEGRATION_MODEL")
	if baseURL == "" || model == "" {
		t.Skip("set LEMONADE_INTEGRATION_BASE_URL and LEMONADE_INTEGRATION_MODEL for live validation")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	router := New(Config{CredentialEncryptionKey: key}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "live-lemonade", Type: "lemonade", BaseURL: baseURL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	credentialID := ""
	if secret := os.Getenv("LEMONADE_INTEGRATION_API_KEY"); secret != "" {
		credentialID = "live-credential"
		if _, err := router.CreateCredential(CredentialInput{ID: credentialID, ProviderID: "live-lemonade", Secret: secret}); err != nil {
			t.Fatal("cannot create isolated test credential")
		}
	}
	probe, err := router.TestProvider(t.Context(), "live-lemonade", credentialID)
	if err != nil || probe.Status != "available" || probe.ModelCount == 0 {
		t.Fatalf("probe=%+v err=%v", probe, err)
	}
	models, err := router.DiscoverProviderModels(t.Context(), "live-lemonade", credentialID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range models {
		if item.ID != model {
			continue
		}
		if item.CapabilitySource != "provider_metadata" {
			t.Fatal("discovery lost capability provenance")
		}
		// Validate the discovered deployment without mutating any persisted state.
		deployment := ModelDeployment{ID: "live-deployment", ProviderID: "live-lemonade", CredentialID: credentialID, Models: []string{"live-public-model"}, UpstreamModel: model, Capabilities: item.Capabilities, Enabled: true}
		if err := router.validateDeployment(deployment); err != nil {
			t.Fatalf("invalid discovered deployment: %v", err)
		}
		t.Logf("model=%s capabilities=%v source=%s", item.ID, item.Capabilities, item.CapabilitySource)
		return
	}
	t.Fatal("requested installed model absent from discovery")
}
