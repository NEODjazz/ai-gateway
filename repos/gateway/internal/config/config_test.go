package config

import "testing"

func TestNormalizeProviderEndpointsLoadsAPIKeyFromEnv(t *testing.T) {
	t.Setenv("PROVIDER_API_KEY_OPENROUTER_KEY_A", "secret-from-env")

	endpoints := normalizeProviderEndpoints([]ProviderEndpointConfig{
		{
			Name: "openrouter-key-a",
			Type: "openai-compatible",
		},
	})

	if len(endpoints) != 1 {
		t.Fatalf("expected one endpoint, got %d", len(endpoints))
	}
	if endpoints[0].APIKey != "secret-from-env" {
		t.Fatalf("expected api key from env, got %q", endpoints[0].APIKey)
	}
}

func TestNormalizeProviderEndpointsKeepsExplicitAPIKey(t *testing.T) {
	t.Setenv("PROVIDER_API_KEY_OPENROUTER_KEY_A", "secret-from-env")

	endpoints := normalizeProviderEndpoints([]ProviderEndpointConfig{
		{
			Name:   "openrouter-key-a",
			Type:   "openai-compatible",
			APIKey: "explicit-secret",
		},
	})

	if endpoints[0].APIKey != "explicit-secret" {
		t.Fatalf("expected explicit api key, got %q", endpoints[0].APIKey)
	}
}
