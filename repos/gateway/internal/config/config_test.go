package config

import (
	"testing"
	"time"
)

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

func TestLoadRoutingAndCacheConfiguration(t *testing.T) {
	t.Setenv("EXACT_CACHE_TTL_SECONDS", "120")
	t.Setenv("EXACT_CACHE_MAX_BYTES", "2048")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("REDIS_DB", "2")
	t.Setenv("REDIS_PREFIX", "tenant-gateway")
	t.Setenv("ROUTING_STRATEGY", "adaptive")
	t.Setenv("ADAPTIVE_ROUTING_EWMA_ALPHA", "0.35")
	t.Setenv("RESPONSES_AFFINITY_TTL_SECONDS", "7200")
	t.Setenv("GUARDRAIL_POLICIES_JSON", `{"strict":{"dlp":true,"av":true}}`)
	t.Setenv("PROVIDERS_JSON", `[{"name":"group-a","type":"demo","model_aliases":{"fast":"upstream-fast"},"weight":3,"capabilities":["chat"]}]`)
	cfg := Load()
	if cfg.Cache.TTLSeconds != 120 || cfg.Cache.MaxBytes != 2048 || !cfg.Provider.GuardrailPolicies["strict"].DLP || !cfg.Provider.GuardrailPolicies["strict"].AV {
		t.Fatalf("unexpected cache/policy config: %+v", cfg)
	}
	if cfg.Redis.Addr != "redis:6379" || cfg.Redis.DB != 2 || cfg.Redis.Prefix != "tenant-gateway" {
		t.Fatalf("unexpected redis config: %+v", cfg.Redis)
	}
	if cfg.Provider.RoutingStrategy != "adaptive" || cfg.Provider.AdaptiveEWMAAlpha != 0.35 || cfg.Provider.AffinityTTL != 2*time.Hour {
		t.Fatalf("unexpected adaptive routing config: %+v", cfg.Provider)
	}
	endpoint := cfg.Provider.Endpoints[0]
	if endpoint.ModelAliases["fast"] != "upstream-fast" || endpoint.Weight != 3 || len(endpoint.Capabilities) != 1 {
		t.Fatalf("unexpected routing config: %+v", endpoint)
	}
}

func TestLoadModelCatalog(t *testing.T) {
	t.Setenv("MODEL_CATALOG_JSON", `{"version":"v1","unknown_model_policy":"deny","models":[{"provider":"demo","model":"model","capabilities":["chat"]}]}`)
	cfg := Load()
	if cfg.InitErr != nil || cfg.Catalog.Version != "v1" || !cfg.Catalog.DenyUnknownModels() {
		t.Fatalf("model catalog was not loaded: catalog=%+v err=%v", cfg.Catalog, cfg.InitErr)
	}
	t.Setenv("MODEL_CATALOG_JSON", `{"models":[{"provider":"demo","model":"model"}]}`)
	if cfg := Load(); cfg.InitErr == nil {
		t.Fatal("invalid model catalog did not fail configuration")
	}
}

func TestLoadTelemetryConfiguration(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "gateway-test")
	t.Setenv("AI_GATEWAY_VERSION", "v1.2.3")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://collector:4318/v1/traces")
	t.Setenv("OTEL_TRACE_SAMPLE_RATIO", "0.25")
	cfg := Load()
	if cfg.Telemetry.ServiceName != "gateway-test" || cfg.Telemetry.Version != "v1.2.3" ||
		cfg.Telemetry.Endpoint != "http://collector:4318/v1/traces" || cfg.Telemetry.SampleRatio != 0.25 {
		t.Fatalf("unexpected telemetry config: %+v", cfg.Telemetry)
	}
}
